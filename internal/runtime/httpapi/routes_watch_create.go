// godex-feature: watch-create-session
// 手表端创建对话（W 系列补齐）：腕上直接开一个新会话，并限定可选的内置模板。
// 入口：POST /watch/sessions、GET /watch/templates
// 文档：docs/wearos-watch-implementation-plan.md
package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/services/backend"
)

// watchDefaultTemplate is used when the watch does not name one.
//
// A wrist cannot meaningfully choose between tool sets, so creating a session
// there must not require a choice: tapping "新建" has to produce a usable session
// immediately. This is the preset that keeps replies short and the tool surface
// read-only, which is what the device can actually work with.
const watchDefaultTemplate = "wear-slim"

// watchTemplateChoices is the curated set offered on the wrist, in display order.
//
// Curated rather than "all templates": the node can hold dozens (builtin, user,
// package-derived), most of which assume a full-size screen. Offering everything
// would turn a two-tap action on a 454px display into a scrolling menu of
// presets the user cannot evaluate there.
var watchTemplateChoices = []string{"wear-slim", "general-assistant", "minimal"}

// registerWatchSessionCreateRoutes covers creating a session from the wrist.
//
// Under `command` scope, not `read`: creating a session mutates node state. A
// read-only watch credential must not be able to spawn work.
func registerWatchSessionCreateRoutes(mux *http.ServeMux, api watchAPI, read, command func(http.Handler) http.Handler) {
	service := api.service

	// The list of presets the wrist may pick from. Read scope: it is a catalogue.
	mux.Handle("GET /watch/templates", read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			writeJSON(w, http.StatusOK, []watchTemplateItem{})
			return
		}
		items := make([]watchTemplateItem, 0, len(watchTemplateChoices))
		for _, id := range watchTemplateChoices {
			tpl, err := service.GetAgentTemplate(id)
			if err != nil {
				// A deployment may have removed a builtin or named a user template
				// over it. Skipping keeps the endpoint useful instead of failing the
				// whole list over one absent preset.
				continue
			}
			items = append(items, watchTemplateItem{
				ID:          tpl.ID,
				Name:        tpl.Name,
				Description: truncateRunes(strings.TrimSpace(tpl.Description), 60),
				Avatar:      tpl.Avatar,
			})
		}
		writeJSON(w, http.StatusOK, items)
	})))

	mux.Handle("POST /watch/sessions", command(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		device, _ := watchDeviceFromContext(r.Context())
		if service == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("session service unavailable"))
			return
		}
		var req watchCreateSessionRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		templateID := strings.TrimSpace(req.TemplateID)
		if templateID == "" {
			templateID = watchDefaultTemplate
		}
		// Validate before creating: an unknown template would otherwise either be
		// ignored (a session that quietly lacks the preset the user picked) or fail
		// after the session already exists.
		if _, err := service.GetAgentTemplate(templateID); err != nil {
			writeError(w, http.StatusBadRequest,
				fmt.Errorf("unknown template %q: %w", templateID, err))
			return
		}

		// A unique key per tap is what makes this *create* rather than *resume*:
		// godex derives the session id from channel+key, so a fixed key would hand
		// back the previous session every time instead of starting a new one.
		locator := backend.SessionLocator{
			Channel: "watch",
			Key:     fmt.Sprintf("%s-%d", deviceKeyFragment(device.DeviceID), time.Now().UnixNano()),
			Metadata: map[string]string{
				"template":        templateID,
				"watch_device_id": device.DeviceID,
			},
		}
		opened, err := service.OpenSession(r.Context(), locator)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}

		// Persist the empty session before answering 201.
		//
		// godex keeps a conversation-less session in memory only, which means
		// GET /watch/sessions (built from persisted manifests) would not contain the
		// id we are about to return. The watch stores that id as its current session
		// and then re-resolves it against the list, so a missing entry silently
		// redirected the user to the previous conversation — "已新建对话" followed by
		// the old chat still on screen.
		//
		// Failing here is reported as an error rather than swallowed: returning 201
		// for a session that cannot be listed is exactly the lie being fixed.
		if err := service.EnsureSessionPersisted(r.Context(), opened.SessionID); err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}

		// A title is optional: an untitled session is derived from its first user
		// message, which on a watch is exactly what the user is about to send. Only
		// an explicit title needs writing.
		if title := strings.TrimSpace(req.Title); title != "" {
			if _, err := service.RenameSession(r.Context(), opened.SessionID, title); err != nil {
				// The session exists and is usable; failing the whole create over a
				// cosmetic label would leave the user with a phantom new session and
				// no id to talk to. Report success and let the title fall back.
				_ = err
			}
		}

		writeJSON(w, http.StatusCreated, watchSessionItem{
			SessionID:      opened.SessionID,
			Title:          strings.TrimSpace(req.Title),
			LastActivityAt: opened.UpdatedAt,
		})
	})))
}

// deviceKeyFragment keeps a device id recognisable in a session key without
// letting an odd id break the key. Session keys are also used as directory names
// downstream, so anything outside a conservative set is replaced.
func deviceKeyFragment(deviceID string) string {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return "watch"
	}
	var b strings.Builder
	for _, r := range deviceID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	// Session ids are shown and copied around; a bounded fragment keeps them
	// readable rather than embedding a whole device identifier.
	out := b.String()
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}
