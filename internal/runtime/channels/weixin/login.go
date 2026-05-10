package weixin

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
)

func Setup(ctx context.Context, cfg *config.Config, stdout io.Writer) error {
	if cfg == nil {
		return fmt.Errorf("missing config")
	}
	weixinCfg := cfg.Weixin
	weixinCfg.AccountID = normalizeAccountID(weixinCfg.AccountID)
	client, err := newTransport(weixinCfg, "")
	if err != nil {
		return err
	}
	store := newStateStore(cfg.StateDir, weixinCfg.AccountID)
	if err := store.Ensure(); err != nil {
		return err
	}

	qr, err := client.GetBotQRCode(ctx)
	if err != nil {
		return fmt.Errorf("request weixin login QR code: %w", err)
	}
	if strings.TrimSpace(qr.QRCode) == "" {
		return fmt.Errorf("weixin login QR response did not include a qrcode token")
	}

	if stdout != nil {
		fmt.Fprintf(stdout, "Weixin QR login for account %s\n", weixinCfg.AccountID)
		if strings.TrimSpace(qr.QRCodeImgURL) != "" {
			fmt.Fprintf(stdout, "QR image URL: %s\n", strings.TrimSpace(qr.QRCodeImgURL))
		}
		if content := strings.TrimSpace(qr.QRCodeImgContent); content != "" && strings.TrimSpace(qr.QRCodeImgURL) == "" {
			if strings.HasPrefix(content, "http://") || strings.HasPrefix(content, "https://") {
				fmt.Fprintf(stdout, "QR image URL: %s\n", content)
			} else {
				fmt.Fprintln(stdout, "QR image content is embedded in the login response; use the QR token if your client needs to re-render it.")
			}
		}
		fmt.Fprintf(stdout, "QR token: %s\n", strings.TrimSpace(qr.QRCode))
		fmt.Fprintln(stdout, "Scan the QR code in Weixin and confirm the login.")
	}

	pollCtx, cancel := context.WithTimeout(ctx, qrSetupTimeout)
	defer cancel()

	var lastStatus string
	ticker := time.NewTicker(qrPollInterval)
	defer ticker.Stop()
	for {
		status, err := client.GetQRCodeStatus(pollCtx, qr.QRCode)
		if err != nil {
			return fmt.Errorf("poll weixin QR login status: %w", err)
		}
		if status.Status != lastStatus && stdout != nil {
			fmt.Fprintf(stdout, "Login status: %s\n", strings.TrimSpace(status.Status))
			lastStatus = status.Status
		}
		switch strings.TrimSpace(status.Status) {
		case "confirmed":
			if strings.TrimSpace(status.BotToken) == "" {
				return fmt.Errorf("weixin login was confirmed but bot_token is empty")
			}
			baseURL := strings.TrimSpace(status.BaseURL)
			if baseURL == "" {
				baseURL = strings.TrimSpace(weixinCfg.BaseURL)
			}
			if baseURL == "" {
				baseURL = defaultBaseURL
			}
			if err := store.SaveAccount(&accountState{
				BotToken:    strings.TrimSpace(status.BotToken),
				BaseURL:     baseURL,
				CDNBaseURL:  defaultIfEmpty(strings.TrimSpace(weixinCfg.CDNBaseURL), defaultCDNBaseURL),
				ILinkBotID:  strings.TrimSpace(status.ILinkBotID),
				ILinkUserID: strings.TrimSpace(status.ILinkUserID),
				UpdatedAt:   time.Now(),
			}); err != nil {
				return err
			}
			if err := store.SaveCursor(""); err != nil {
				return err
			}
			if err := store.writeJSON(store.ContextTokensPath(), contextTokensState{
				Tokens:    map[string]string{},
				UpdatedAt: time.Now(),
			}); err != nil {
				return err
			}
			if stdout != nil {
				fmt.Fprintf(stdout, "Weixin login completed for account %s\n", weixinCfg.AccountID)
				if !weixinCfg.Enabled {
					fmt.Fprintln(stdout, "Login state was saved, but channels.weixin.enabled is still false.")
					fmt.Fprintln(stdout, "Set channels.weixin.enabled: true in godex.yaml or start with WEIXIN_ENABLED=true before running `godex serve`.")
				}
			}
			return nil
		case "expired":
			return fmt.Errorf("weixin login QR code expired before confirmation")
		}

		select {
		case <-pollCtx.Done():
			return fmt.Errorf("timed out waiting for Weixin QR confirmation")
		case <-ticker.C:
		}
	}
}

func Logout(ctx context.Context, cfg *config.Config, stdout io.Writer) error {
	_ = ctx
	if cfg == nil {
		return fmt.Errorf("missing config")
	}
	store := newStateStore(cfg.StateDir, cfg.Weixin.AccountID)
	if err := store.RemoveAll(); err != nil {
		return err
	}
	if stdout != nil {
		fmt.Fprintf(stdout, "Weixin login state cleared for account %s\n", normalizeAccountID(cfg.Weixin.AccountID))
	}
	return nil
}

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
