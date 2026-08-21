package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
)

const (
	envFileExample = "# Optional local secrets. Copy to .env and fill values as needed.\nANTHROPIC_API_KEY=\n"
)

// RunConfigWizard provides an interactive CLI to configure LLM providers.
// It walks the user through adding, removing, and listing providers,
// then persists changes to godex.yaml and .env.
func RunConfigWizard(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = ctx

	// Find workspace and config
	workspace, _ := os.Getwd()
	configPath := filepath.Join(workspace, "godex.yaml")

	// Try to load existing config manager, or create a fresh one
	manager, err := config.NewManager(config.Options{WorkspaceDir: workspace, ConfigPath: configPath})
	if err != nil {
		// No config yet — create a minimal one first
		if err := config.WriteDefaultConfigFile(configPath, false); err != nil && !os.IsExist(err) {
			return fmt.Errorf("failed to create config file: %w", err)
		}
		manager, err = config.NewManager(config.Options{WorkspaceDir: workspace, ConfigPath: configPath})
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	cfg := manager.Current()
	_ = cfg.EnsureDirs()

	reader := bufio.NewReader(stdin)

	fmt.Fprintln(stdout, "╔══════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(stdout, "║          GoDex Provider Configuration Wizard                ║")
	fmt.Fprintln(stdout, "╚══════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "This wizard will help you set up one or more LLM providers.")
	fmt.Fprintln(stdout, "You need at least one provider configured for GoDex to work.")
	fmt.Fprintln(stdout)

	for {
		providers := cfg.LLMProviders
		if len(providers) > 0 {
			fmt.Fprintln(stdout, "── Current providers ──")
			for id, p := range providers {
				fmt.Fprintf(stdout, "  • %s (%s) → %s [%d model(s)]\n", id, p.Name, p.BaseURL, len(p.Models))
			}
			fmt.Fprintln(stdout)
		} else {
			fmt.Fprintln(stdout, "  (no providers configured yet)")
			fmt.Fprintln(stdout)
		}

		fmt.Fprintln(stdout, "What would you like to do?")
		fmt.Fprintln(stdout, "  [a] Add a provider")
		fmt.Fprintln(stdout, "  [i] Import from other coding agents (Codex/Claude Code/etc.)")
		if len(providers) > 0 {
			fmt.Fprintln(stdout, "  [r] Remove a provider")
		}
		fmt.Fprintln(stdout, "  [q] Quit and save")
		fmt.Fprint(stdout, "> ")

		choice, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("input error: %w", err)
		}
		choice = strings.TrimSpace(strings.ToLower(choice))

		switch choice {
		case "a", "add":
			if err := interactiveAddProvider(reader, stdout, stderr, manager); err != nil {
				fmt.Fprintf(stderr, "Error: %v\n", err)
			}
		case "i", "import":
			if err := interactiveImportCodex(reader, stdout, stderr, manager); err != nil {
				fmt.Fprintf(stderr, "Error: %v\n", err)
			}
			// Refresh config after import.
			cfg = manager.Current()
		case "r", "remove":
			if len(providers) == 0 {
				fmt.Fprintln(stdout, "No providers to remove.")
				continue
			}
			if err := interactiveRemoveProvider(reader, stdout, stderr, manager); err != nil {
				fmt.Fprintf(stderr, "Error: %v\n", err)
			}
		case "q", "quit", "":
			fmt.Fprintln(stdout)
			fmt.Fprintln(stdout, "Configuration saved. Run 'godex doctor' to verify.")
			return nil
		default:
			fmt.Fprintln(stdout, "Invalid choice. Please enter a, i, r, or q.")
		}
		fmt.Fprintln(stdout)
	}
}

func interactiveAddProvider(reader *bufio.Reader, stdout, stderr io.Writer, manager *config.Manager) error {
	cfg := manager.Current()

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "── Add a new LLM provider ──")
	fmt.Fprintln(stdout)

	// Step 1: Provider ID
	fmt.Fprintln(stdout, "Provider ID (short key used in config, e.g. 'openai', 'anthropic'):")
	providerID, err := promptRequired(reader, stdout, "Provider ID")
	if err != nil {
		return err
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))

	// Check for duplicates
	existing := cfg.LLMProviders
	if _, exists := existing[providerID]; exists {
		fmt.Fprintf(stdout, "Provider '%s' already exists. Use remove first to replace it.\n", providerID)
		return nil
	}

	// Step 2: Provider type
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Provider type:")
	fmt.Fprintln(stdout, "  1. openai_compatible    (OpenAI, DeepSeek, Ollama, Groq, etc.)")
	fmt.Fprintln(stdout, "  2. anthropic_compatible (Anthropic Claude)")
	fmt.Fprintln(stdout, "  (default: openai_compatible)")
	fmt.Fprint(stdout, "> ")
	typeChoice, _ := reader.ReadString('\n')
	typeChoice = strings.TrimSpace(typeChoice)

	var providerType string
	switch typeChoice {
	case "2", "anthropic", "anthropic_compatible":
		providerType = llm.ProviderAnthropicCompatible
	default:
		providerType = llm.ProviderOpenAICompatible
	}
	fmt.Fprintf(stdout, "  → Using type: %s\n", providerType)

	// Step 3: Display name
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "Display name (e.g. 'OpenAI', 'Anthropic') [%s]:\n", providerID)
	name, _ := reader.ReadString('\n')
	name = strings.TrimSpace(name)
	if name == "" {
		name = providerID
	}

	// Step 4: Base URL
	var defaultBaseURL string
	switch providerType {
	case llm.ProviderAnthropicCompatible:
		defaultBaseURL = "https://api.anthropic.com"
	default:
		defaultBaseURL = "https://api.openai.com"
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "Base URL [%s]:\n", defaultBaseURL)
	fmt.Fprintln(stdout, "  (Do NOT include trailing /v1 — GoDex appends protocol paths)")
	fmt.Fprint(stdout, "> ")
	baseURL, _ := reader.ReadString('\n')
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	// Step 5: API Key
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "API Key configuration:")
	fmt.Fprintln(stdout, "  For security, we recommend using an environment variable.")
	fmt.Fprintln(stdout, "  e.g. create a .env file and put OPENAI_API_KEY=sk-xxx there.")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "  Options:")
	fmt.Fprintln(stdout, "    1. Use environment variable (recommended)")
	fmt.Fprintln(stdout, "    2. Enter API key directly (⚠️ will be written to godex.yaml)")
	fmt.Fprint(stdout, "  Choose [1]: ")
	keyChoice, _ := reader.ReadString('\n')
	keyChoice = strings.TrimSpace(keyChoice)

	var apiKey, apiKeyEnv string
	if keyChoice == "2" {
		fmt.Fprint(stdout, "  API Key (input will be visible): ")
		apiKey, _ = reader.ReadString('\n')
		apiKey = strings.TrimSpace(apiKey)
	} else {
		// Default: env var
		var defaultEnv string
		switch providerID {
		case "openai":
			defaultEnv = "OPENAI_API_KEY"
		case "anthropic":
			defaultEnv = "ANTHROPIC_API_KEY"
		case "deepseek":
			defaultEnv = "DEEPSEEK_API_KEY"
		case "groq":
			defaultEnv = "GROQ_API_KEY"
		case "ollama":
			defaultEnv = ""
		case "openrouter":
			defaultEnv = "OPENROUTER_API_KEY"
		case "moonshot":
			defaultEnv = "MOONSHOT_API_KEY"
		default:
			defaultEnv = strings.ToUpper(providerID) + "_API_KEY"
		}
		fmt.Fprintf(stdout, "  Environment variable name [%s]: ", defaultEnv)
		apiKeyEnv, _ = reader.ReadString('\n')
		apiKeyEnv = strings.TrimSpace(apiKeyEnv)
		if apiKeyEnv == "" {
			apiKeyEnv = defaultEnv
		}

		if apiKeyEnv != "" {
			// Offer to add to .env
			fmt.Fprintf(stdout, "  Add %s to .env file? (y/n) [y]: ", apiKeyEnv)
			addEnv, _ := reader.ReadString('\n')
			addEnv = strings.TrimSpace(strings.ToLower(addEnv))
			if addEnv == "" || addEnv == "y" || addEnv == "yes" {
				workspace := cfg.WorkspaceDir
				envPath := filepath.Join(workspace, ".env")
				// Ensure .env exists
				f, openErr := os.OpenFile(envPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
				if openErr == nil {
					// Check if env var already exists in .env
					content, readErr := os.ReadFile(envPath)
					if readErr == nil && !strings.Contains(string(content), apiKeyEnv+"=") {
						fmt.Fprintf(f, "%s=\n", apiKeyEnv)
						fmt.Fprintf(stdout, "  → Added %s to %s (fill in your key there)\n", apiKeyEnv, envPath)
					}
					f.Close()
				}
			}
		}
	}

	// Step 6: Models
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "── Model configuration ──")
	fmt.Fprintln(stdout, "You need at least one model. Add models one at a time.")

	models := make(map[string]llm.ModelConfig)

	for {
		fmt.Fprintln(stdout)

		var defaultModelID, defaultModelStr string
		switch {
		case providerType == llm.ProviderAnthropicCompatible:
			defaultModelID = "sonnet"
			defaultModelStr = "claude-sonnet-4-20250514"
		case providerID == "deepseek":
			defaultModelID = "v3"
			defaultModelStr = "deepseek-chat"
		case providerID == "ollama":
			defaultModelID = "llama3"
			defaultModelStr = "llama3"
		default:
			defaultModelID = "gpt4o"
			defaultModelStr = "gpt-4o"
		}

		fmt.Fprintf(stdout, "Model ID (config key, e.g. '%s') [%s]: ", defaultModelID, defaultModelID)
		modelID, _ := reader.ReadString('\n')
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			modelID = defaultModelID
		}

		fmt.Fprintf(stdout, "Model string (API model name, e.g. '%s') [%s]: ", defaultModelStr, defaultModelStr)
		modelStr, _ := reader.ReadString('\n')
		modelStr = strings.TrimSpace(modelStr)
		if modelStr == "" {
			modelStr = defaultModelStr
		}

		fmt.Fprint(stdout, "Max tokens [8192]: ")
		maxTokensStr, _ := reader.ReadString('\n')
		maxTokensStr = strings.TrimSpace(maxTokensStr)
		maxTokens := 8192
		if maxTokensStr != "" {
			fmt.Sscanf(maxTokensStr, "%d", &maxTokens)
		}

		models[modelID] = llm.ModelConfig{
			Name:      modelID,
			Model:     modelStr,
			MaxTokens: maxTokens,
		}
		fmt.Fprintf(stdout, "  → Added model: %s → %s (max %d tokens)\n", modelID, modelStr, maxTokens)

		fmt.Fprint(stdout, "Add another model? (y/n) [n]: ")
		more, _ := reader.ReadString('\n')
		more = strings.TrimSpace(strings.ToLower(more))
		if more != "y" && more != "yes" {
			break
		}
	}

	if len(models) == 0 {
		return fmt.Errorf("at least one model is required")
	}

	// Build provider config
	provider := llm.ProviderConfig{
		Name:      name,
		Type:      providerType,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		APIKeyEnv: apiKeyEnv,
		Models:    models,
	}

	// Update config
	allProviders := cfg.LLMProviders
	allProviders[providerID] = provider
	if err := manager.UpdateProviders(allProviders); err != nil {
		return fmt.Errorf("failed to save provider: %w", err)
	}

	fmt.Fprintf(stdout, "\n✓ Provider '%s' added with %d model(s).\n", providerID, len(models))
	return nil
}

func interactiveRemoveProvider(reader *bufio.Reader, stdout, stderr io.Writer, manager *config.Manager) error {
	cfg := manager.Current()
	providers := cfg.LLMProviders

	if len(providers) == 0 {
		fmt.Fprintln(stdout, "No providers to remove.")
		return nil
	}

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "── Remove a provider ──")
	fmt.Fprintln(stdout, "Existing providers:")
	ids := make([]string, 0, len(providers))
	for id := range providers {
		ids = append(ids, id)
	}
	for i, id := range ids {
		fmt.Fprintf(stdout, "  %d. %s (%s)\n", i+1, id, providers[id].Name)
	}

	fmt.Fprint(stdout, "Enter provider ID to remove: ")
	removeID, _ := reader.ReadString('\n')
	removeID = strings.TrimSpace(removeID)

	if _, exists := providers[removeID]; !exists {
		fmt.Fprintf(stdout, "Provider '%s' not found.\n", removeID)
		return nil
	}

	fmt.Fprintf(stdout, "Remove '%s'? (y/n) [n]: ", removeID)
	confirm, _ := reader.ReadString('\n')
	confirm = strings.TrimSpace(strings.ToLower(confirm))
	if confirm != "y" && confirm != "yes" {
		fmt.Fprintln(stdout, "Cancelled.")
		return nil
	}

	delete(providers, removeID)
	if err := manager.UpdateProviders(providers); err != nil {
		return fmt.Errorf("failed to remove provider: %w", err)
	}

	fmt.Fprintf(stdout, "✓ Provider '%s' removed.\n", removeID)
	return nil
}

func promptRequired(reader *bufio.Reader, stdout io.Writer, label string) (string, error) {
	for {
		fmt.Fprintf(stdout, "%s: ", label)
		val, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		val = strings.TrimSpace(val)
		if val != "" {
			return val, nil
		}
		fmt.Fprintf(stdout, "  %s is required. Please enter a value.\n", label)
	}
}

func interactiveImportCodex(reader *bufio.Reader, stdout, stderr io.Writer, manager *config.Manager) error {
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "── Import providers from Codex ──")
	fmt.Fprintln(stdout)

	codexConfigPath := config.DefaultCodexConfigPath()
	if codexConfigPath == "" {
		return fmt.Errorf("cannot determine Codex config path (HOME not set)")
	}

	fmt.Fprintf(stdout, "Codex config: %s\n", codexConfigPath)
	if _, err := os.Stat(codexConfigPath); os.IsNotExist(err) {
		return fmt.Errorf("Codex config not found at %s", codexConfigPath)
	}

	imported, err := config.ImportCodexProviders(codexConfigPath, "")
	if err != nil {
		return fmt.Errorf("failed to read Codex config: %w", err)
	}

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Discovered provider(s) from Codex:")
	for i, p := range imported {
		fmt.Fprintf(stdout, "  %d. [%s] %s | type=%s | base_url=%s [%d model(s)]\n",
			i+1, p.ProviderID, p.ProviderConfig.Name,
			p.ProviderConfig.Type, p.ProviderConfig.BaseURL,
			len(p.ProviderConfig.Models))
		for _, m := range p.Models {
			fmt.Fprintf(stdout, "       • %s → %s\n", m.ID, m.Model)
		}
	}

	// Check for conflicts with existing providers.
	cfg := manager.Current()
	existing := cfg.LLMProviders
	for _, p := range imported {
		// Prefix codex providers to avoid name collisions.
		targetID := "codex-" + p.ProviderID
		if _, exists := existing[targetID]; exists {
			fmt.Fprintf(stdout, "\n⚠ Provider '%s' already exists and will be skipped.\n", targetID)
		}
	}

	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, "Import these provider(s)? This will merge into existing config. (y/n) [y]: ")
	confirm, _ := reader.ReadString('\n')
	confirm = strings.TrimSpace(strings.ToLower(confirm))
	if confirm == "n" || confirm == "no" {
		fmt.Fprintln(stdout, "Cancelled.")
		return nil
	}

	// Merge: keep existing providers, add/overwrite imported ones.
	merged := make(map[string]llm.ProviderConfig, len(existing)+len(imported))
	for id, p := range existing {
		merged[id] = p
	}
	added := 0
	skipped := 0
	for _, p := range imported {
		targetID := "codex-" + p.ProviderID
		if _, exists := existing[targetID]; exists {
			skipped++
			continue
		}
		merged[targetID] = p.ProviderConfig
		added++
	}

	if added == 0 {
		if skipped > 0 {
			return fmt.Errorf("all providers already exist — nothing to import")
		}
		return fmt.Errorf("no providers to import")
	}

	if err := manager.UpdateProviders(merged); err != nil {
		return fmt.Errorf("failed to save providers: %w", err)
	}

	fmt.Fprintf(stdout, "\n✓ Imported %d provider(s) from Codex.", added)
	if skipped > 0 {
		fmt.Fprintf(stdout, " (%d skipped as duplicates)", skipped)
	}
	fmt.Fprintln(stdout)
	return nil
}
