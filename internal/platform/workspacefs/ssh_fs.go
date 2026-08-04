package workspacefs

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"github.com/spf13/afero"
	"github.com/spf13/afero/sftpfs"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHConfig controls SSH connection creation for SFTP-backed workspaces.
type SSHConfig struct {
	Target     string   // user@host[:port]
	Workspace  string   // remote workspace directory (absolute)
	SSHOptions []string // extra ssh options (passed as -o Key=Value)
	Timeout    time.Duration
}

// NewSSHFS creates a workspacefs.FS backed by SFTP over SSH.
// The caller must close the returned FS when done.
func NewSSHFS(cfg SSHConfig) (FS, error) {
	target := strings.TrimSpace(cfg.Target)
	if target == "" {
		return nil, fmt.Errorf("ssh target is required")
	}
	workspace := strings.TrimSpace(cfg.Workspace)
	if workspace == "" {
		return nil, fmt.Errorf("ssh workspace is required")
	}

	// Parse target: user@host[:port]
	user, host, port, err := parseSSHTarget(target)
	if err != nil {
		return nil, fmt.Errorf("invalid ssh target %q: %w", target, err)
	}

	// Build SSH client config
	sshCfg := &ssh.ClientConfig{
		User:            user,
		HostKeyCallback: buildHostKeyCallback(),
		Timeout:         cfg.Timeout,
	}
	if sshCfg.Timeout == 0 {
		sshCfg.Timeout = 10 * time.Second
	}

	// Try SSH agent first, then default key files
	if auth := sshAgentAuth(); auth != nil {
		sshCfg.Auth = append(sshCfg.Auth, auth)
	}
	if auth := defaultKeyAuth(); auth != nil {
		sshCfg.Auth = append(sshCfg.Auth, auth)
	}
	if len(sshCfg.Auth) == 0 {
		return nil, fmt.Errorf("no ssh authentication method available (agent or ~/.ssh/id_*)")
	}

	// Connect
	addr := net.JoinHostPort(host, port)
	client, err := ssh.Dial("tcp", addr, sshCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}

	// Open SFTP session
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("sftp session: %w", err)
	}

	// Wrap in afero BasePathFs rooted at the remote workspace
	baseFs := sftpfs.New(sftpClient)
	rootedFs := afero.NewBasePathFs(baseFs, workspace)

	fs, err := NewWithConfig(Config{
		Backend:      BackendAfero,
		WorkspaceDir: workspace,
		AferoFs:      rootedFs,
	})
	if err != nil {
		sftpClient.Close()
		client.Close()
		return nil, fmt.Errorf("create afero fs: %w", err)
	}

	return &sshFS{
		FS:         fs,
		sftpClient: sftpClient,
		sshClient:  client,
	}, nil
}

// sshFS wraps the afero-backed FS and owns the underlying SSH/SFTP connections.
type sshFS struct {
	FS
	sftpClient *sftp.Client
	sshClient  *ssh.Client
}

func (f *sshFS) Close() error {
	if f.sftpClient != nil {
		f.sftpClient.Close()
	}
	if f.sshClient != nil {
		f.sshClient.Close()
	}
	return f.FS.Close()
}

// parseSSHTarget splits user@host[:port] into components.
func parseSSHTarget(target string) (user, host, port string, err error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", "", "", fmt.Errorf("empty target")
	}

	// Split user@host
	if idx := strings.LastIndex(target, "@"); idx >= 0 {
		user = target[:idx]
		target = target[idx+1:]
	}

	// Split host:port
	if idx := strings.LastIndex(target, ":"); idx >= 0 {
		host = target[:idx]
		port = target[idx+1:]
	} else {
		host = target
		port = "22"
	}

	if user == "" {
		user = os.Getenv("USER")
	}
	if host == "" {
		return "", "", "", fmt.Errorf("missing host")
	}
	return user, host, port, nil
}

// buildHostKeyCallback returns a host key callback that accepts any key
// (for development) or verifies against known_hosts if available.
func buildHostKeyCallback() ssh.HostKeyCallback {
	// Try known_hosts first
	home, err := os.UserHomeDir()
	if err == nil {
		knownHostsPath := filepath.Join(home, ".ssh", "known_hosts")
		if _, err := os.Stat(knownHostsPath); err == nil {
			callback, err := knownhosts.New(knownHostsPath)
			if err == nil {
				return callback
			}
		}
	}
	// Fall back to insecure (accept any) — suitable for development
	return ssh.InsecureIgnoreHostKey()
}

// sshAgentAuth returns an auth method from the SSH agent if available.
func sshAgentAuth() ssh.AuthMethod {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil
	}
	agentClient := agent.NewClient(conn)
	signers, err := agentClient.Signers()
	if err != nil || len(signers) == 0 {
		conn.Close()
		return nil
	}
	return ssh.PublicKeys(signers...)
}

// defaultKeyAuth returns an auth method from ~/.ssh/id_rsa, id_ed25519, etc.
func defaultKeyAuth() ssh.AuthMethod {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	keyFiles := []string{
		"id_ed25519",
		"id_rsa",
		"id_ecdsa",
		"id_dsa",
	}
	var signers []ssh.Signer
	for _, name := range keyFiles {
		path := filepath.Join(home, ".ssh", name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			continue
		}
		signers = append(signers, signer)
	}
	if len(signers) == 0 {
		return nil
	}
	return ssh.PublicKeys(signers...)
}
