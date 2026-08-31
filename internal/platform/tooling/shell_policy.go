package tooling

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

func SplitCommandLine(input string) ([]string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty command")
	}

	args := make([]string, 0)
	var current strings.Builder
	var quote rune
	runes := []rune(input)

	flush := func() {
		if current.Len() == 0 {
			return
		}
		args = append(args, current.String())
		current.Reset()
	}

	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			if r == '\\' {
				if next, ok := escapedRune(runes, i, quote); ok {
					current.WriteRune(next)
					i++
					continue
				}
			}
			current.WriteRune(r)
			continue
		}

		switch {
		case r == '\\':
			if next, ok := escapedRune(runes, i, 0); ok {
				current.WriteRune(next)
				i++
				continue
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	flush()
	if len(args) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	return args, nil
}

func escapedRune(runes []rune, index int, quote rune) (rune, bool) {
	if index+1 >= len(runes) {
		return 0, false
	}
	next := runes[index+1]
	if next == '\\' || next == '\'' || next == '"' || unicode.IsSpace(next) {
		if quote == 0 || next == quote || next == '\\' || unicode.IsSpace(next) {
			return next, true
		}
	}
	return 0, false
}

func validateShellCommand(command string) error {
	return validateShellCommandWithOptions(command, ShellCommandOptions{})
}

// ValidateShellCommand applies the same shell safety checks used before local,
// Docker, or SSH execution. It is intended for declaration-time diagnostics
// that must not execute the command.
func ValidateShellCommand(command string) error {
	return validateShellCommand(command)
}

// ValidateShellCommandWithPolicy applies shell validation with explicit policy
// options. It is used by safety profiles and package smoke diagnostics.
func ValidateShellCommandWithPolicy(command string, options ShellCommandOptions) error {
	return validateShellCommandWithOptions(command, options)
}

func validateShellCommandWithOptions(command string, options ShellCommandOptions) error {
	if err := validateShellPatterns(command, options); err != nil {
		return err
	}
	if options.DenyHighRisk {
		if risk := ClassifyShellCommandRisk(command); risk.Level == ShellRiskHigh {
			return fmt.Errorf("high-risk shell command denied: %s", risk.Reason)
		}
	}
	if err := validateCommandSubstitutions(command, options); err != nil {
		return err
	}
	segments, err := splitShellCommand(command)
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return fmt.Errorf("empty command")
	}

	for _, segment := range segments {
		name, err := firstShellCommandName(segment)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		argv, err := SplitCommandLine(segment)
		if err != nil {
			return err
		}
		if err := validateCommandSafety(name, argv, options); err != nil {
			return err
		}
		if !options.AllowUnlistedCommands && !allowedCommandWithOptions(name, options) {
			return fmt.Errorf("command not allowed: %s", name)
		}
	}
	return nil
}

func validateArgvCommandWithOptions(argv []string, options ShellCommandOptions) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	command := strings.Join(argv, " ")
	if err := validateShellPatterns(command, options); err != nil {
		return err
	}
	if options.DenyHighRisk {
		if risk := ClassifyShellCommandRisk(command); risk.Level == ShellRiskHigh {
			return fmt.Errorf("high-risk shell command denied: %s", risk.Reason)
		}
	}
	if err := validateCommandSubstitutions(command, options); err != nil {
		return err
	}
	name := strings.TrimSpace(argv[0])
	if name == "" {
		return fmt.Errorf("empty command")
	}
	if err := validateCommandSafety(name, argv, options); err != nil {
		return err
	}
	if !options.AllowUnlistedCommands && !allowedCommandWithOptions(name, options) {
		return fmt.Errorf("command not allowed: %s", name)
	}
	return nil
}

func validateShellPatterns(command string, options ShellCommandOptions) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	for _, pattern := range options.DenyPatterns {
		if shellPatternMatches(pattern, command) {
			return fmt.Errorf("shell command denied by policy pattern %q", pattern)
		}
	}
	if len(options.AllowPatterns) == 0 {
		return nil
	}
	for _, pattern := range options.AllowPatterns {
		if shellPatternMatches(pattern, command) {
			return nil
		}
	}
	return fmt.Errorf("shell command does not match any allowed policy pattern")
}

func shellPatternMatches(pattern, command string) bool {
	pattern = strings.TrimSpace(pattern)
	command = strings.TrimSpace(command)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if ok, err := filepath.Match(pattern, command); err == nil && ok {
		return true
	}
	if strings.HasSuffix(pattern, "*") && strings.HasPrefix(command, strings.TrimSuffix(pattern, "*")) {
		return true
	}
	return strings.EqualFold(pattern, command)
}

type ShellRiskLevel string

const (
	ShellRiskLow    ShellRiskLevel = "low"
	ShellRiskMedium ShellRiskLevel = "medium"
	ShellRiskHigh   ShellRiskLevel = "high"
)

type ShellRisk struct {
	Level  ShellRiskLevel
	Reason string
}

func ClassifyShellCommandRisk(command string) ShellRisk {
	command = strings.TrimSpace(command)
	if command == "" {
		return ShellRisk{Level: ShellRiskLow}
	}
	lower := strings.ToLower(command)
	switch {
	case strings.Contains(lower, "<(") || strings.Contains(lower, ">("):
		return ShellRisk{Level: ShellRiskHigh, Reason: "process substitution can hide executed input"}
	case downloadsPipedToShell(lower):
		return ShellRisk{Level: ShellRiskHigh, Reason: "downloaded content is piped directly into a shell"}
	case base64DecodedToShell(lower):
		return ShellRisk{Level: ShellRiskHigh, Reason: "base64-decoded content is piped directly into a shell"}
	}
	segments, err := splitShellCommand(command)
	if err != nil {
		return ShellRisk{Level: ShellRiskHigh, Reason: err.Error()}
	}
	for _, segment := range segments {
		argv, err := SplitCommandLine(segment)
		if err != nil || len(argv) == 0 {
			continue
		}
		name := filepath.Base(strings.ToLower(strings.TrimSpace(argv[0])))
		switch name {
		case "python", "python3", "node":
			if inlineExecFlag(argv[1:]) {
				return ShellRisk{Level: ShellRiskHigh, Reason: name + " inline code execution requires review"}
			}
		case "ruby", "perl", "php":
			if inlineExecFlag(argv[1:]) {
				return ShellRisk{Level: ShellRiskHigh, Reason: name + " inline code execution requires review"}
			}
		}
	}
	return ShellRisk{Level: ShellRiskLow}
}

func downloadsPipedToShell(command string) bool {
	parts := strings.Split(command, "|")
	if len(parts) < 2 {
		return false
	}
	for idx := 0; idx < len(parts)-1; idx++ {
		left := strings.TrimSpace(parts[idx])
		right := strings.TrimSpace(parts[idx+1])
		if (strings.HasPrefix(left, "curl ") || strings.HasPrefix(left, "wget ")) && startsShell(right) {
			return true
		}
	}
	return false
}

func base64DecodedToShell(command string) bool {
	parts := strings.Split(command, "|")
	if len(parts) < 2 {
		return false
	}
	for idx := 0; idx < len(parts)-1; idx++ {
		left := strings.TrimSpace(parts[idx])
		right := strings.TrimSpace(parts[idx+1])
		if strings.Contains(left, "base64") && (strings.Contains(left, "-d") || strings.Contains(left, "--decode")) && startsShell(right) {
			return true
		}
	}
	return false
}

func startsShell(command string) bool {
	command = strings.TrimSpace(command)
	return strings.HasPrefix(command, "sh") || strings.HasPrefix(command, "bash") || strings.HasPrefix(command, "zsh") ||
		strings.HasPrefix(command, "cmd") || strings.HasPrefix(command, "powershell") || strings.HasPrefix(command, "pwsh")
}

func inlineExecFlag(args []string) bool {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "-c" || arg == "-e" || arg == "/c" || arg == "/C" {
			return true
		}
		if strings.HasPrefix(arg, "-") && (strings.Contains(arg, "c") || strings.Contains(arg, "e")) {
			return true
		}
	}
	return false
}

func DisallowedShellCommands(command string) ([]string, error) {
	return DisallowedShellCommandsWithOptions(command, ShellCommandOptions{})
}

func DisallowedShellCommandsWithOptions(command string, options ShellCommandOptions) ([]string, error) {
	segments, err := splitShellCommand(command)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var names []string
	for _, segment := range segments {
		name, err := firstShellCommandName(segment)
		if err != nil {
			return nil, err
		}
		if name == "" || allowedCommandWithOptions(name, options) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names, nil
}

func validateCommandSafety(name string, argv []string, options ShellCommandOptions) error {
	base := filepath.Base(strings.TrimSpace(name))
	switch base {
	case "sudo", "su", "shutdown", "reboot", "halt", "poweroff", "mkfs", "mount", "umount":
		return fmt.Errorf("dangerous shell command denied: %s", base)
	case "rm":
		if hasRecursiveForce(argv[1:]) && targetsRoot(argv[1:]) {
			return fmt.Errorf("dangerous shell command denied: rm -rf targeting root")
		}
	case "sh", "bash":
		if script := shellScriptArg(argv[1:]); script != "" {
			if err := validateShellCommandWithOptions(script, options); err != nil {
				return err
			}
		}
	case "cmd":
		if script := cmdScriptArg(argv[1:]); script != "" {
			if err := validateShellCommandWithOptions(script, options); err != nil {
				return err
			}
		}
	case "powershell", "pwsh":
		if script := powershellScriptArg(argv[1:]); script != "" {
			if err := validateShellCommandWithOptions(script, options); err != nil {
				return err
			}
		}
	}
	for _, arg := range argv[1:] {
		if err := validateShellURLArg(arg); err != nil {
			return err
		}
	}
	if isLocalPathSensitiveCommand(base, argv) {
		if err := validateWorkspacePathArgs(options.WorkspaceDir, base, argv[1:]); err != nil {
			return err
		}
	}
	return nil
}

func shellScriptArg(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "-c":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		case strings.HasPrefix(arg, "-") && strings.Contains(arg, "c"):
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
	}
	return ""
}

func cmdScriptArg(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "/c" || arg == "/C":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
	}
	return ""
}

func powershellScriptArg(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "-Command" || arg == "-command" || arg == "-c":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
	}
	return ""
}

func hasRecursiveForce(args []string) bool {
	recursive := false
	force := false
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") || arg == "-" || arg == "--" {
			continue
		}
		if strings.Contains(arg, "r") || strings.Contains(arg, "R") {
			recursive = true
		}
		if strings.Contains(arg, "f") {
			force = true
		}
	}
	return recursive && force
}

func targetsRoot(args []string) bool {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == "/" || arg == "/*" || arg == "/." || arg == "/.." {
			return true
		}
		// Windows root check (e.g., C:\, D:\)
		if len(arg) >= 2 && arg[1] == ':' && (len(arg) == 2 || arg[2] == '\\' || arg[2] == '/') {
			cleaned := filepath.Clean(arg)
			if len(cleaned) <= 3 { // e.g., "C:\" or "C:"
				return true
			}
		}
		cleaned := filepath.Clean(strings.TrimRight(arg, "/*\\"))
		if cleaned == string(os.PathSeparator) || cleaned == "~" {
			return true
		}
	}
	return false
}

func validateShellURLArg(arg string) error {
	raw := strings.Trim(strings.TrimSpace(arg), `"'`)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil
	}
	host := strings.ToLower(parsed.Hostname())
	if isMetadataHost(host) {
		return fmt.Errorf("shell command URL targets cloud metadata host: %s", host)
	}
	ip := net.ParseIP(host)
	if ip != nil && isPrivateOrLocalIP(ip) {
		return fmt.Errorf("shell command URL targets private or local network address: %s", host)
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("shell command URL targets private or local network address: %s", host)
	}
	return nil
}

func isMetadataHost(host string) bool {
	switch host {
	case "169.254.169.254", "169.254.170.2", "100.100.100.200", "metadata", "metadata.google.internal":
		return true
	default:
		return false
	}
}

func isPrivateOrLocalIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func isLocalPathSensitiveCommand(name string, argv []string) bool {
	switch name {
	case "rm", "cp", "mv", "chmod", "chown", "mkdir", "touch":
		return true
	case "sed":
		for _, arg := range argv[1:] {
			if arg == "-i" || strings.HasPrefix(arg, "-i") {
				return true
			}
		}
	}
	return false
}

func validateWorkspacePathArgs(workspace, command string, args []string) error {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		arg = strings.Trim(strings.TrimSpace(arg), `"'`)
		if arg == "" || arg == "--" {
			continue
		}
		if redirectionConsumesNextToken(arg) {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "-") || isShellRedirectionToken(arg) || looksLikeRemotePath(arg) || looksLikeURL(arg) {
			continue
		}
		if !isPotentialPath(arg) {
			continue
		}
		candidate := arg
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(workspaceAbs, candidate)
		}
		cleaned, err := filepath.Abs(candidate)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(workspaceAbs, cleaned)
		if err != nil {
			return err
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("%s path escapes workspace: %s", command, arg)
		}
	}
	return nil
}

func looksLikeURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

func looksLikeRemotePath(value string) bool {
	if strings.Contains(value, "://") {
		return true
	}
	idx := strings.IndexByte(value, ':')
	if idx <= 0 {
		return false
	}
	// Windows drive letter (e.g., C:\) is not a remote path
	if idx == 1 && len(value) > 2 && (value[2] == '\\' || value[2] == '/') {
		return false
	}
	return !strings.Contains(value[:idx], string(os.PathSeparator))
}

func isPotentialPath(value string) bool {
	return strings.HasPrefix(value, ".") || strings.HasPrefix(value, "~") || filepath.IsAbs(value) || strings.Contains(value, string(os.PathSeparator))
}

func splitShellCommand(command string) ([]string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, nil
	}

	segments := make([]string, 0, 4)
	current := make([]byte, 0, len(command))
	inSingle := false
	inDouble := false
	escaped := false

	appendSegment := func() {
		segment := strings.TrimSpace(string(current))
		current = current[:0]
		if segment != "" {
			segments = append(segments, segment)
		}
	}

	for i := 0; i < len(command); i++ {
		ch := command[i]

		if escaped {
			current = append(current, ch)
			escaped = false
			continue
		}

		if inSingle {
			current = append(current, ch)
			if ch == '\'' {
				inSingle = false
			}
			continue
		}

		if ch == '`' {
			return nil, fmt.Errorf("command substitution is not allowed")
		}
		if (ch == '<' || ch == '>') && i+1 < len(command) && command[i+1] == '(' {
			return nil, fmt.Errorf("process substitution is not allowed")
		}

		if inDouble {
			current = append(current, ch)
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inDouble = false
			}
			continue
		}

		switch ch {
		case '\\':
			current = append(current, ch)
			escaped = true
		case '\'':
			current = append(current, ch)
			inSingle = true
		case '"':
			current = append(current, ch)
			inDouble = true
		case '&':
			if i+1 < len(command) && command[i+1] == '&' {
				appendSegment()
				i++
				continue
			}
			if i+1 < len(command) && command[i+1] == '>' {
				current = append(current, ch)
				continue
			}
			if prev := lastNonSpaceByte(current); prev == '>' || prev == '<' {
				current = append(current, ch)
				continue
			}
			return nil, fmt.Errorf("background execution is not allowed")
		case '|':
			appendSegment()
			if i+1 < len(command) && command[i+1] == '|' {
				i++
			}
		case ';', '\n':
			appendSegment()
		default:
			current = append(current, ch)
		}
	}

	if escaped {
		return nil, fmt.Errorf("unfinished escape sequence")
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quote")
	}

	appendSegment()
	return segments, nil
}

// validateCommandSubstitutions enforces the command-substitution policy for a
// command. When RelaxSubstitutionAll is set (yolo/trusted), every $(...) is
// allowed. Otherwise when RelaxCommandSubstitution is set (default agent bash
// path), each substitution's inner command must pass the normal safety chain.
// When neither flag is set, any $(...) is rejected, preserving strict behavior
// for callers that do not opt in.
func validateCommandSubstitutions(command string, options ShellCommandOptions) error {
	if options.RelaxSubstitutionAll {
		return nil
	}
	substitutions := extractCommandSubstitutions(command)
	if len(substitutions) == 0 {
		return nil
	}
	if !options.RelaxCommandSubstitution {
		return fmt.Errorf("command substitution is not allowed")
	}
	for _, inner := range substitutions {
		if err := validateSubstitutionInner(inner, options); err != nil {
			return err
		}
	}
	return nil
}

// validateSubstitutionInner validates a single $(...) inner command. It reuses
// the risk classifier and the dangerous-command checks so read-only commands
// (git rev-parse, date, pwd, echo) pass while inline interpreters, downloads
// piped to a shell, nested substitution, process substitution, and dangerous
// base commands (sudo, rm -rf on root, sensitive URLs) are rejected.
func validateSubstitutionInner(inner string, options ShellCommandOptions) error {
	if risk := commandSubstitutionRisk(inner, options); risk.Level == ShellRiskHigh {
		return fmt.Errorf("command substitution denied: %s", risk.Reason)
	}
	return nil
}

// commandSubstitutionRisk classifies a $(...) inner command without executing
// it. It returns ShellRiskHigh for nested/process substitution, inline
// interpreter code, downloads piped to a shell, and dangerous base commands.
func commandSubstitutionRisk(inner string, options ShellCommandOptions) ShellRisk {
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return ShellRisk{Level: ShellRiskHigh, Reason: "empty command substitution"}
	}
	if strings.Contains(inner, "$(") || strings.Contains(inner, "`") ||
		strings.Contains(inner, "<(") || strings.Contains(inner, ">(") {
		return ShellRisk{Level: ShellRiskHigh, Reason: "nested command substitution"}
	}
	if risk := ClassifyShellCommandRisk(inner); risk.Level == ShellRiskHigh {
		return risk
	}
	argv, err := SplitCommandLine(inner)
	if err != nil || len(argv) == 0 {
		return ShellRisk{Level: ShellRiskHigh, Reason: "unparseable command substitution"}
	}
	name := filepath.Base(strings.TrimSpace(argv[0]))
	if err := validateCommandSafety(name, argv, options); err != nil {
		return ShellRisk{Level: ShellRiskHigh, Reason: err.Error()}
	}
	return ShellRisk{Level: ShellRiskLow}
}

// extractCommandSubstitutions returns the inner command text of every $(...)
// command substitution in source order. Arithmetic expansion $((...)) is
// treated as non-substitution and skipped. `$(` inside single quotes is a
// literal (not expanded by the shell) and is skipped.
func extractCommandSubstitutions(command string) []string {
	var out []string
	inSingle := false
	for i := 0; i < len(command); i++ {
		ch := command[i]
		if ch == '\'' && (i == 0 || command[i-1] != '\\') {
			inSingle = !inSingle
			continue
		}
		if inSingle {
			continue
		}
		if ch != '$' || i+1 >= len(command) || command[i+1] != '(' {
			continue
		}
		if i+2 < len(command) && command[i+2] == '(' {
			continue // arithmetic expansion $((...))
		}
		inner, end := extractBalancedParen(command, i+1)
		if end < 0 {
			continue
		}
		out = append(out, inner)
		i = end
	}
	return out
}

// extractBalancedParen extracts the text between the opening paren at open (the
// index of '(') and its matching ')'. It tracks nesting, quotes, and escapes.
// Returns the inner text and the index of the matching ')', or ("", -1) when
// the input is unbalanced. The paren at open is the outer boundary and does not
// count toward nesting depth.
func extractBalancedParen(command string, open int) (string, int) {
	depth := 0
	var inner strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	for j := open + 1; j < len(command); j++ {
		ch := command[j]
		if escaped {
			inner.WriteByte(ch)
			escaped = false
			continue
		}
		switch {
		case ch == '\\':
			inner.WriteByte(ch)
			escaped = true
		case inSingle:
			inner.WriteByte(ch)
			if ch == '\'' {
				inSingle = false
			}
		case ch == '\'':
			inner.WriteByte(ch)
			inSingle = true
		case inDouble:
			inner.WriteByte(ch)
			if ch == '"' {
				inDouble = false
			}
		case ch == '"':
			inner.WriteByte(ch)
			inDouble = true
		case ch == '(':
			depth++
			inner.WriteByte(ch)
		case ch == ')':
			if depth == 0 {
				return strings.TrimSpace(inner.String()), j
			}
			depth--
			inner.WriteByte(ch)
		default:
			inner.WriteByte(ch)
		}
	}
	return "", -1
}

func firstShellCommandName(segment string) (string, error) {
	argv, err := SplitCommandLine(segment)
	if err != nil {
		return "", err
	}

	skipNext := false
	for _, token := range argv {
		if skipNext {
			skipNext = false
			continue
		}
		switch {
		case token == "":
		case isShellEnvAssignment(token):
		case redirectionConsumesNextToken(token):
			skipNext = true
		case isShellRedirectionToken(token):
		default:
			return token, nil
		}
	}
	return "", nil
}

func isShellEnvAssignment(token string) bool {
	idx := strings.IndexByte(token, '=')
	if idx <= 0 {
		return false
	}
	name := token[:idx]
	for i, r := range name {
		switch {
		case r == '_':
		case unicode.IsLetter(r):
		case unicode.IsDigit(r) && i > 0:
		default:
			return false
		}
	}
	return true
}

func redirectionConsumesNextToken(token string) bool {
	switch token {
	case "<", ">", ">>", "<<", "<<<", "<>", "1>", "1>>", "1<", "2>", "2>>", "2<", "&>", "&>>", ">&", "<&":
		return true
	default:
		return false
	}
}

func isShellRedirectionToken(token string) bool {
	if token == "" {
		return false
	}
	if redirectionConsumesNextToken(token) {
		return true
	}
	if strings.HasPrefix(token, "&>") {
		return true
	}
	i := 0
	for i < len(token) && token[i] >= '0' && token[i] <= '9' {
		i++
	}
	if i < len(token) && (token[i] == '<' || token[i] == '>') {
		return true
	}
	return strings.HasPrefix(token, "<") || strings.HasPrefix(token, ">")
}

func allowedCommand(name string) bool {
	name = filepath.Base(strings.TrimSpace(name))
	allowed := map[string]bool{
		"ls": true, "cd": true, "pwd": true, "cat": true, "head": true,
		"tail": true, "grep": true, "rg": true, "find": true, "mkdir": true, "rm": true,
		"cp": true, "mv": true, "touch": true, "chmod": true, "git": true, "sed": true,
		"go": true, "python": true, "pip": true, "npm": true, "npx": true, "node": true,
		"pnpm": true, "yarn": true, "bun": true, "playwright-cli": true,
		"curl": true, "wget": true, "docker": true, "make": true, "sh": true,
		"bash": true, "jq": true, "diff": true, "echo": true, "printf": true,
		"ssh": true, "scp": true, "rsync": true,
		// Windows-specific:
		"cmd": true, "powershell": true, "pwsh": true, "type": true,
		"where": true, "attrib": true, "xcopy": true, "robocopy": true,
		"tasklist": true, "taskkill": true, "findstr": true, "more": true,
		"sort": true, "fc": true, "comp": true, "systeminfo": true,
		"ver": true, "ipconfig": true, "ping": true, "tracert": true,
		"netstat": true, "nslookup": true, "whoami": true, "cscript": true,
	}
	return allowed[name]
}

func allowedCommandWithOptions(name string, options ShellCommandOptions) bool {
	if allowedCommand(name) {
		return true
	}
	name = filepath.Base(strings.TrimSpace(name))
	for _, candidate := range options.AllowedCommands {
		if strings.EqualFold(name, filepath.Base(strings.TrimSpace(candidate))) {
			return true
		}
	}
	return false
}

func normalizeShellPatterns(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func appendDockerEnvArgs(args []string) []string {
	for _, item := range minimalCommandEnv("") {
		if strings.TrimSpace(item) == "" {
			continue
		}
		args = append(args, "-e", item)
	}
	return args
}

// InheritedCommandEnv returns the complete GoDex process environment for a
// local child process, with working-directory variables and explicit overrides
// applied without duplicate keys. Local shell entry points use this so exported
// toolchain, proxy, credential, and application variables remain available.
func InheritedCommandEnv(workingDir string, overrides ...string) []string {
	env := append([]string{}, os.Environ()...)
	if strings.TrimSpace(workingDir) != "" {
		if runtime.GOOS == "windows" {
			overrides = append([]string{"CD=" + workingDir}, overrides...)
		} else {
			overrides = append([]string{"PWD=" + workingDir}, overrides...)
		}
	}

	indexes := make(map[string]int, len(env)+len(overrides))
	for i, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if ok && key != "" {
			indexes[commandEnvKey(key)] = i
		}
	}
	for _, item := range overrides {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		normalized := commandEnvKey(key)
		if i, exists := indexes[normalized]; exists {
			env[i] = item
			continue
		}
		indexes[normalized] = len(env)
		env = append(env, item)
	}
	return env
}

func commandEnvKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}

// minimalCommandEnv is intentionally restricted to isolation boundaries such
// as Docker, where forwarding every host variable could leak secrets.
func minimalCommandEnv(workingDir string) []string {
	keepExact := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
		"TMPDIR": true, "TEMP": true, "TMP": true, "LANG": true, "TERM": true,
		"GOCACHE": true, "GOMODCACHE": true, "GOPATH": true, "GOROOT": true,
		"NODE_OPTIONS": true, "NPM_CONFIG_CACHE": true, "PNPM_HOME": true,
		// Windows-specific:
		"USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
		"COMPUTERNAME": true, "COMSPEC": true, "PATHEXT": true,
		"SYSTEMROOT": true, "WINDIR": true, "ALLUSERSPROFILE": true,
		"PROCESSOR_ARCHITECTURE": true, "NUMBER_OF_PROCESSORS": true,
		"OS": true,
	}
	env := make([]string, 0, len(keepExact)+4)
	seen := map[string]struct{}{}
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		if keepExact[key] || strings.HasPrefix(key, "LC_") {
			env = append(env, item)
			seen[key] = struct{}{}
		}
	}
	if _, ok := seen["PATH"]; !ok {
		if runtime.GOOS == "windows" {
			env = append(env, "PATH=C:\\Windows\\system32;C:\\Windows;C:\\Windows\\System32\\Wbem")
		} else {
			env = append(env, "PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
		}
	}
	if strings.TrimSpace(workingDir) != "" {
		if runtime.GOOS == "windows" {
			env = append(env, "CD="+workingDir)
		} else {
			env = append(env, "PWD="+workingDir)
		}
	}
	return env
}

func expandHomeArgs(args []string) ([]string, error) {
	if len(args) == 0 {
		return args, nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}

	expanded := make([]string, len(args))
	for i, arg := range args {
		switch {
		case arg == "~":
			expanded[i] = homeDir
		case strings.HasPrefix(arg, "~/"):
			expanded[i] = filepath.Join(homeDir, strings.TrimPrefix(arg, "~/"))
		default:
			expanded[i] = arg
		}
	}
	return expanded, nil
}

func preview(text string) string {
	if len(text) <= 50 {
		return text
	}
	return text[:50]
}

func shellExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ProcessState != nil {
		return exitErr.ProcessState.ExitCode()
	}
	return -1
}

func lastNonSpaceByte(data []byte) byte {
	for i := len(data) - 1; i >= 0; i-- {
		if !unicode.IsSpace(rune(data[i])) {
			return data[i]
		}
	}
	return 0
}

func cloneMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return map[string]interface{}{}
	}
	result := make(map[string]interface{}, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
