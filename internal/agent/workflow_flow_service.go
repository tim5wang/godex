package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/flow"
	"github.com/tim5wang/godex/internal/tools"
)

// Keep the exact-match form separate from workflowVarRefRegex: exact JSON
// placeholders preserve their original JSON type instead of becoming strings.
var workflowServiceExactVarRef = regexp.MustCompile(`^\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}$`)

type workflowServiceNetworkPolicy struct {
	flowNetworkPolicy
	AllowPrivateHosts bool
}

type workflowServiceFailure struct {
	message   string
	retryKind string
}

func (e *workflowServiceFailure) Error() string {
	return e.message
}

func serviceNetworkPolicyFromSpec(np *flow.NetworkPolicy) workflowServiceNetworkPolicy {
	out := workflowServiceNetworkPolicy{
		flowNetworkPolicy: flowNetworkPolicyFromSpec(np),
	}
	if np != nil {
		out.AllowPrivateHosts = np.AllowPrivateHosts
	}
	return out
}

// executeWorkflowService runs one synchronous HTTP(S) JSON call. Request
// credentials and bodies are never copied into workflow events or errors.
func (a *Agent) executeWorkflowService(ctx context.Context, state *workflowState, node *workflowNode) {
	spec := node.ServiceSpec
	if spec == nil {
		a.failWorkflowServiceNode(state, node, "service node missing spec")
		return
	}
	if node.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(node.TimeoutSec)*time.Second)
		defer cancel()
	}

	start := time.Now()
	outputs, err := a.callWorkflowService(ctx, *state, spec)
	latency := time.Since(start)
	if err != nil {
		var failure *workflowServiceFailure
		if errors.As(err, &failure) {
			if retry, delay := shouldWorkflowRetry(node.Retry, node.Attempt, failure.retryKind); retry {
				a.scheduleWorkflowNodeRetry(state, node, node.Attempt, failure.Error(), failure.retryKind, delay)
				return
			}
			a.failWorkflowServiceNode(state, node, failure.Error())
			return
		}
		a.failWorkflowServiceNode(state, node, "service request failed")
		return
	}
	a.completeWorkflowService(state, node, outputs, latency)
}

func (a *Agent) callWorkflowService(
	ctx context.Context,
	state workflowState,
	spec *workflowServiceSpec,
) (map[string]any, error) {
	if err := validateWorkflowServiceRuntimeSpec(spec); err != nil {
		return nil, err
	}
	rawURL, err := renderWorkflowServiceString(state, spec.URL)
	if err != nil {
		return nil, err
	}
	parsed, policy, err := validateWorkflowServiceURL(ctx, rawURL, spec.Network)
	if err != nil {
		return nil, err
	}
	method := strings.ToUpper(strings.TrimSpace(spec.Method))

	var requestBody io.Reader
	if len(spec.Body) > 0 {
		var body any
		decoder := json.NewDecoder(bytes.NewReader(spec.Body))
		decoder.UseNumber()
		if err := decoder.Decode(&body); err != nil {
			return nil, &workflowServiceFailure{message: "service request body is invalid JSON"}
		}
		body, err = renderWorkflowServiceValue(state, body)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, &workflowServiceFailure{message: "service request body could not be encoded"}
		}
		requestBody = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), requestBody)
	if err != nil {
		return nil, &workflowServiceFailure{message: "service request URL or method is invalid"}
	}
	for name, value := range spec.Headers {
		value, err = renderWorkflowServiceString(state, value)
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, &workflowServiceFailure{message: "service header value contains a line break"}
		}
		req.Header.Set(name, value)
	}
	if requestBody != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if err := applyWorkflowServiceAuth(req, spec.Auth); err != nil {
		return nil, err
	}

	timeout := time.Duration(policy.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil, // do not let environment proxies bypass destination checks
			DialContext:           workflowServiceDialContext(dialer, policy),
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			ExpectContinueTimeout: time.Second,
		},
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if len(via) > 0 && !sameWorkflowServiceOrigin(via[0].URL, next.URL) {
				// A custom API-key header is not a standard sensitive header, so
				// never forward it to a different origin.
				return http.ErrUseLastResponse
			}
			_, _, err := validateWorkflowServiceURL(next.Context(), next.URL.String(), spec.Network)
			return err
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, &workflowServiceFailure{message: "service request timed out", retryKind: retryKindProviderTimeout}
			}
			return nil, &workflowServiceFailure{message: "service request canceled"}
		}
		var serviceFailure *workflowServiceFailure
		if errors.As(err, &serviceFailure) {
			return nil, serviceFailure
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, &workflowServiceFailure{message: "service request timed out", retryKind: retryKindProviderTimeout}
		}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, &workflowServiceFailure{message: "service host could not be resolved"}
		}
		return nil, &workflowServiceFailure{message: "service request failed", retryKind: retryKindToolTransient}
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		failure := &workflowServiceFailure{message: fmt.Sprintf("service returned HTTP status %d", resp.StatusCode)}
		switch {
		case resp.StatusCode == http.StatusRequestTimeout:
			failure.retryKind = retryKindProviderTimeout
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError:
			failure.retryKind = retryKindProviderError
		}
		return nil, failure
	}

	maxResponseBytes := policy.MaxResponseChars
	if maxResponseBytes <= 0 {
		maxResponseBytes = 1 << 20
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxResponseBytes)+1))
	if err != nil {
		return nil, &workflowServiceFailure{message: "service response could not be read", retryKind: retryKindToolTransient}
	}
	if len(responseBody) > maxResponseBytes {
		return nil, &workflowServiceFailure{message: "service response exceeded the configured size limit"}
	}

	var response any
	if len(bytes.TrimSpace(responseBody)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(responseBody))
		decoder.UseNumber()
		if err := decoder.Decode(&response); err != nil {
			return nil, &workflowServiceFailure{message: "service response was not valid JSON"}
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, &workflowServiceFailure{message: "service response contained trailing data"}
		}
	}
	return map[string]any{
		"status_code": resp.StatusCode,
		"body":        response,
	}, nil
}

func validateWorkflowServiceRuntimeSpec(spec *workflowServiceSpec) error {
	if spec == nil {
		return &workflowServiceFailure{message: "service node missing spec"}
	}
	if err := flow.ValidateServiceSpec(&flow.ServiceSpec{
		Method:  spec.Method,
		URL:     spec.URL,
		Headers: spec.Headers,
		Body:    spec.Body,
		Auth:    spec.Auth,
	}); err != nil {
		return &workflowServiceFailure{message: "service specification is invalid"}
	}
	return nil
}

func validateWorkflowServiceURL(
	ctx context.Context,
	rawURL string,
	network *flow.NetworkPolicy,
) (*url.URL, workflowServiceNetworkPolicy, error) {
	policy := serviceNetworkPolicyFromSpec(network)
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Hostname() == "" || parsed.User != nil {
		return nil, policy, &workflowServiceFailure{message: "service URL must be an absolute HTTP(S) URL"}
	}
	if _, err := workflowServiceAllowedIPs(ctx, parsed.Hostname(), policy); err != nil {
		return nil, policy, err
	}
	return parsed, policy, nil
}

func workflowServiceAllowedIPs(
	ctx context.Context,
	host string,
	policy workflowServiceNetworkPolicy,
) ([]net.IP, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, &workflowServiceFailure{message: "service URL is missing a hostname"}
	}
	lowerHost := strings.ToLower(host)
	if tools.MatchDomainPattern(lowerHost, policy.BlockedDomains) {
		return nil, &workflowServiceFailure{message: "service host is blocked by Flow network policy"}
	}
	if policy.Policy == "allowlist" && !tools.MatchDomainPattern(lowerHost, policy.AllowedDomains) {
		return nil, &workflowServiceFailure{message: "service host is not in the Flow allowlist"}
	}
	if policy.AllowPrivateHosts && policy.Policy != "allowlist" {
		return nil, &workflowServiceFailure{message: "private service hosts require an explicit Flow allowlist"}
	}

	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		var err error
		ips, err = net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, &workflowServiceFailure{message: "service host could not be resolved"}
		}
	}
	if len(ips) == 0 {
		return nil, &workflowServiceFailure{message: "service host resolved to no addresses"}
	}
	for _, ip := range ips {
		if !policy.AllowPrivateHosts && tools.IsPrivateOrLocalIP(ip) {
			return nil, &workflowServiceFailure{message: "private or local service addresses are blocked"}
		}
	}
	return ips, nil
}

func workflowServiceDialContext(
	dialer *net.Dialer,
	policy workflowServiceNetworkPolicy,
) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, &workflowServiceFailure{message: "service destination is invalid"}
		}
		ips, err := workflowServiceAllowedIPs(ctx, host, policy)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			return nil, &workflowServiceFailure{message: "service host could not be reached"}
		}
		return nil, lastErr
	}
}

func sameWorkflowServiceOrigin(left, right *url.URL) bool {
	if left == nil || right == nil || !strings.EqualFold(left.Scheme, right.Scheme) ||
		!strings.EqualFold(left.Hostname(), right.Hostname()) {
		return false
	}
	leftPort := left.Port()
	rightPort := right.Port()
	if leftPort == "" {
		if strings.EqualFold(left.Scheme, "https") {
			leftPort = "443"
		} else {
			leftPort = "80"
		}
	}
	if rightPort == "" {
		if strings.EqualFold(right.Scheme, "https") {
			rightPort = "443"
		} else {
			rightPort = "80"
		}
	}
	return leftPort == rightPort
}

func applyWorkflowServiceAuth(req *http.Request, auth *flow.ServiceAuthSpec) error {
	if auth == nil {
		return nil
	}
	tokenEnv := strings.TrimSpace(auth.TokenEnv)
	secret := os.Getenv(tokenEnv)
	if secret == "" {
		return &workflowServiceFailure{message: "service credential environment variable is not set: " + tokenEnv}
	}
	if strings.ContainsAny(secret, "\r\n") {
		return &workflowServiceFailure{message: "service credential contains an invalid line break"}
	}
	switch strings.ToLower(strings.TrimSpace(auth.Type)) {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+secret)
	case "api_key":
		req.Header.Set(auth.HeaderName, secret)
	default:
		return &workflowServiceFailure{message: "service authentication type is invalid"}
	}
	return nil
}

func renderWorkflowServiceString(state workflowState, text string) (string, error) {
	if match := workflowServiceExactVarRef.FindStringSubmatch(text); len(match) == 2 {
		value, ok := resolveWorkflowServiceVarRef(state, match[1])
		if !ok {
			return "", &workflowServiceFailure{message: "service variable reference is unresolved: " + match[1]}
		}
		return varRefValue(value), nil
	}
	var renderErr error
	rendered := workflowVarRefRegex.ReplaceAllStringFunc(text, func(ref string) string {
		if renderErr != nil {
			return ref
		}
		expr := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(ref, "{{"), "}}"))
		value, ok := resolveWorkflowServiceVarRef(state, expr)
		if !ok {
			renderErr = &workflowServiceFailure{message: "service variable reference is unresolved: " + expr}
			return ref
		}
		return varRefValue(value)
	})
	return rendered, renderErr
}

func renderWorkflowServiceValue(state workflowState, value any) (any, error) {
	switch current := value.(type) {
	case string:
		if match := workflowServiceExactVarRef.FindStringSubmatch(current); len(match) == 2 {
			resolved, ok := resolveWorkflowServiceVarRef(state, match[1])
			if !ok {
				return nil, &workflowServiceFailure{message: "service variable reference is unresolved: " + match[1]}
			}
			return resolved, nil
		}
		return renderWorkflowServiceString(state, current)
	case []any:
		out := make([]any, len(current))
		for i, item := range current {
			rendered, err := renderWorkflowServiceValue(state, item)
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(current))
		for key, item := range current {
			rendered, err := renderWorkflowServiceValue(state, item)
			if err != nil {
				return nil, err
			}
			out[key] = rendered
		}
		return out, nil
	default:
		return value, nil
	}
}

func resolveWorkflowServiceVarRef(state workflowState, expression string) (any, bool) {
	parts := strings.Split(strings.TrimSpace(expression), ".")
	if len(parts) == 2 && parts[0] == "inputs" {
		value, ok := state.Summary.RunInputs[parts[1]]
		return value, ok
	}
	if len(parts) >= 4 && parts[0] == "nodes" && parts[2] == "outputs" {
		for _, node := range state.Nodes {
			if node.ID != parts[1] {
				continue
			}
			return workflowOutputValue(node.Outputs, strings.Join(parts[3:], "."))
		}
	}
	return nil, false
}

func (a *Agent) failWorkflowServiceNode(state *workflowState, node *workflowNode, message string) {
	now := time.Now().UTC()
	node.Status = workflowStatusError
	node.Error = message
	node.JobID = ""
	node.UpdatedAt = now
	node.FinishedAt = now
	if handoffErr := a.finalizeWorkflowNodeHandoff(state, node, nil, ""); handoffErr != nil {
		node.Error = handoffErr.Error()
	}
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]any{
		"event":   "service_failed",
		"node_id": node.ID,
		"error":   node.Error,
		"at":      now,
	})
}

func (a *Agent) completeWorkflowService(
	state *workflowState,
	node *workflowNode,
	result map[string]any,
	latency time.Duration,
) {
	if err := validateWorkflowOutputSpecs(node.OutputSpec, result, "node "+node.ID+" outputs"); err != nil {
		a.failWorkflowServiceNode(state, node, "output contract violation: "+err.Error())
		return
	}
	now := time.Now().UTC()
	node.Status = workflowStatusCompleted
	node.Outputs = result
	node.Error = ""
	node.JobID = ""
	node.UpdatedAt = now
	node.FinishedAt = now
	payload, _ := json.Marshal(result)
	node.ResultPreview = previewSubagentResultForModel(string(payload))
	if err := a.finalizeWorkflowNodeHandoff(state, node, nil, string(payload)); err != nil {
		node.Status = workflowStatusError
		node.Error = err.Error()
		return
	}
	a.runPostScript(state, node)
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]any{
		"event":       "service_completed",
		"node_id":     node.ID,
		"status_code": result["status_code"],
		"latency_ms":  latency.Milliseconds(),
		"at":          now,
	})
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]any{
		"event":      "node_completed",
		"node_id":    node.ID,
		"latency_ms": a.workflowNodeLatency(*node, now),
		"at":         now,
	})
}
