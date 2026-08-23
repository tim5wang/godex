package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

func normalizeBrowserConfig(cfg config.BrowserConfig) config.BrowserConfig {
	if cfg.ActionTimeoutSeconds <= 0 {
		cfg.ActionTimeoutSeconds = 30
	}
	if cfg.IdleTimeoutSeconds < 0 {
		cfg.IdleTimeoutSeconds = 0
	}
	if cfg.MaxPagesPerSession <= 0 {
		cfg.MaxPagesPerSession = 3
	}
	return cfg
}

func mapKey(key string) input.Key {
	switch strings.TrimSpace(key) {
	case "Enter":
		return input.Enter
	case "Tab":
		return input.Tab
	case "Escape":
		return input.Escape
	case "Backspace":
		return input.Backspace
	case "Delete":
		return input.Delete
	case "ArrowUp":
		return input.ArrowUp
	case "ArrowDown":
		return input.ArrowDown
	case "ArrowLeft":
		return input.ArrowLeft
	case "ArrowRight":
		return input.ArrowRight
	case "Home":
		return input.Home
	case "End":
		return input.End
	case "PageUp":
		return input.PageUp
	case "PageDown":
		return input.PageDown
	case "Space":
		return input.Space
	default:
		if len(key) == 1 {
			return input.Key(key[0])
		}
		return input.Enter
	}
}

func locatorFromArgs(args browserArgs) BrowserLocator {
	return BrowserLocator{
		Ref:          strings.TrimSpace(args.Ref),
		Selector:     strings.TrimSpace(args.Selector),
		Text:         strings.TrimSpace(args.MatchText),
		Placeholder:  strings.TrimSpace(args.Placeholder),
		Label:        strings.TrimSpace(args.Label),
		Tag:          strings.TrimSpace(args.Tag),
		HrefContains: strings.TrimSpace(args.HrefContains),
		InputType:    strings.TrimSpace(args.InputType),
	}
}

func locatorFromField(field BrowserFormField) BrowserLocator {
	return BrowserLocator{
		Ref:         strings.TrimSpace(field.Ref),
		Selector:    strings.TrimSpace(field.Selector),
		Text:        strings.TrimSpace(field.Text),
		Placeholder: strings.TrimSpace(field.Placeholder),
		Label:       strings.TrimSpace(field.Label),
		Tag:         strings.TrimSpace(field.Tag),
		InputType:   strings.TrimSpace(field.InputType),
	}
}

func decodeEvalJSONString(result *proto.RuntimeRemoteObject, target any) error {
	if result == nil {
		return fmt.Errorf("empty browser evaluation result")
	}
	payload := result.Value.String()
	var raw string
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		raw = payload
	}
	return json.Unmarshal([]byte(raw), target)
}

func sanitizeDownloadFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, ":", "_")
	return name
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func snapshotScript(maxChars int) string {
	if maxChars <= 0 {
		maxChars = 4000
	}
	return fmt.Sprintf(`() => {
  function cssPath(el) {
    if (!(el instanceof Element)) return "";
    if (el.id) return "#" + CSS.escape(el.id);
    const path = [];
    let current = el;
    while (current && current.nodeType === Node.ELEMENT_NODE && current !== document.body && current.parentNode !== document) {
      let selector = current.nodeName.toLowerCase();
      if (current.classList && current.classList.length > 0) {
        selector += "." + Array.from(current.classList).slice(0, 2).map((c) => CSS.escape(c)).join(".");
      }
      const siblings = current.parentNode ? Array.from(current.parentNode.children).filter((node) => node.nodeName === current.nodeName) : [];
      if (siblings.length > 1) {
        selector += ":nth-of-type(" + (siblings.indexOf(current) + 1) + ")";
      }
      path.unshift(selector);
      current = current.parentElement;
    }
    return path.join(" > ");
  }
  function visible(el) {
    const rect = el.getBoundingClientRect();
    const style = window.getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
  }
  function nodeText(el) {
    return (el.innerText || el.textContent || "").trim().slice(0, 160);
  }
  function a11yOf(el) {
    const role = el.getAttribute("role") || "";
    const ariaLabel = el.getAttribute("aria-label") || "";
    let ariaChecked = el.getAttribute("aria-checked") || "";
    if (!ariaChecked) {
      if (el.getAttribute("checked") !== null) ariaChecked = "true";
      else if (el.getAttribute("aria-selected") !== null) ariaChecked = el.getAttribute("aria-selected");
    }
    if (!ariaLabel && !role && el.id) {
      const lab = document.querySelector('label[for="' + CSS.escape(el.id) + '"]');
      if (lab) ariaLabel = (lab.innerText || lab.textContent || "").trim();
    }
    return { role, ariaLabel, ariaChecked };
  }
  const INTERACTIVE = 'a,button,input,textarea,select,summary,[role="button"],[role="link"],[role="textbox"],[role="checkbox"],[role="radio"],[role="tab"],[role="menuitem"],[role="switch"],[tabindex],[contenteditable="true"],option';
  function collectIn(root) {
    if (!root) return [];
    const out = [];
    let nodes = [];
    try { nodes = Array.from(root.querySelectorAll(INTERACTIVE)).filter((el) => visible(el)); } catch (e) {}
    for (const el of nodes) {
      const a = a11yOf(el);
      out.push({ el, a });
    }
    // Pierce open shadow roots recursively.
    let all = [];
    try { all = Array.from(root.querySelectorAll("*")); } catch (e) {}
    for (const node of all) {
      if (node.shadowRoot) out.push(...collectIn(node.shadowRoot));
    }
    return out;
  }
  function collectIframes(doc) {
    let frames = [];
    try { frames = Array.from(doc.querySelectorAll("iframe")); } catch (e) {}
    const out = [];
    for (const frame of frames) {
      try {
        if (frame.contentDocument) out.push(...collectIn(frame.contentDocument));
      } catch (e) { /* cross-origin frame */ }
    }
    return out;
  }
  const collected = collectIn(document).concat(collectIframes(document));
  const elements = collected.slice(0, 50).map((item, idx) => ({
    ref: "e" + (idx + 1),
    selector: cssPath(item.el),
    tag: item.el.tagName.toLowerCase(),
    text: nodeText(item.el),
    type: item.el.getAttribute("type") || "",
    href: item.el.getAttribute("href") || "",
    role: item.a.role,
    aria_label: item.a.ariaLabel,
    aria_checked: item.a.ariaChecked
  }));
  let hasCanvas = false;
  try {
    hasCanvas = !!document.querySelector("canvas");
    for (const item of collected) {
      if (item.el.tagName.toLowerCase() === "canvas") { hasCanvas = true; break; }
    }
  } catch (e) {}
  return JSON.stringify({
    title: document.title || "",
    url: location.href,
    text: (document.body ? (document.body.innerText || "") : "").trim().slice(0, %d),
    has_canvas: hasCanvas,
    needs_screenshot: hasCanvas,
    elements
  });
}`, maxChars)
}

func findElementsScript(locator BrowserLocator, limit int) string {
	if limit <= 0 {
		limit = 10
	}
	return fmt.Sprintf(`() => {
  const locator = %s;
  const limit = %d;
  function cssPath(el) {
    if (!(el instanceof Element)) return "";
    if (el.id) return "#" + CSS.escape(el.id);
    const path = [];
    let current = el;
    while (current && current.nodeType === Node.ELEMENT_NODE && current !== document.body && current.parentNode !== document) {
      let selector = current.nodeName.toLowerCase();
      const siblings = current.parentNode ? Array.from(current.parentNode.children).filter((node) => node.nodeName === current.nodeName) : [];
      if (siblings.length > 1) {
        selector += ":nth-of-type(" + (siblings.indexOf(current) + 1) + ")";
      }
      path.unshift(selector);
      current = current.parentElement;
    }
    return path.join(" > ");
  }
  function visible(el) {
    const rect = el.getBoundingClientRect();
    const style = window.getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
  }
  function textOf(el) {
    return ((el.innerText || el.textContent || "").trim()).slice(0, 160);
  }
  function labelText(el) {
    const labels = [];
    if (el.id) {
      labels.push(...document.querySelectorAll('label[for="' + CSS.escape(el.id) + '"]'));
    }
    const wrapping = el.closest("label");
    if (wrapping) labels.push(wrapping);
    return Array.from(new Set(labels)).map((node) => (node.innerText || node.textContent || "").trim()).join(" ").toLowerCase();
  }
  const needleText = (locator.text || "").toLowerCase();
  const needlePlaceholder = (locator.placeholder || "").toLowerCase();
  const needleLabel = (locator.label || "").toLowerCase();
  const needleHref = (locator.href_contains || "").toLowerCase();
  const tagName = (locator.tag || "").toLowerCase();
  const inputType = (locator.input_type || "").toLowerCase();
  const INTERACTIVE = 'a,button,input,textarea,select,summary,[role="button"],[role="link"],[role="textbox"],[role="checkbox"],[role="radio"],[role="tab"],[role="menuitem"],[role="switch"],[tabindex],[contenteditable="true"],label,option';
  function collectIn(root) {
    if (!root) return [];
    const out = [];
    let nodes = [];
    try {
      nodes = locator.selector
        ? Array.from(root.querySelectorAll(locator.selector))
        : Array.from(root.querySelectorAll(INTERACTIVE));
    } catch (err) {
      return [{ error: err instanceof Error ? err.message : String(err) }];
    }
    out.push(...nodes);
    let all = [];
    try { all = Array.from(root.querySelectorAll("*")); } catch (e) {}
    for (const node of all) {
      if (node.shadowRoot) out.push(...collectIn(node.shadowRoot));
    }
    return out;
  }
  function collectAll() {
    let out = [];
    try { out = collectIn(document); } catch (e) {}
    let frames = [];
    try { frames = Array.from(document.querySelectorAll("iframe")); } catch (e) {}
    for (const frame of frames) {
      try {
        if (frame.contentDocument) out = out.concat(collectIn(frame.contentDocument));
      } catch (e) { /* cross-origin */ }
    }
    return out;
  }
  const matches = collectAll().filter((el) => {
    if (!(el instanceof Element) || !visible(el)) return false;
    const tag = (el.tagName || "").toLowerCase();
    const elType = (el.getAttribute("type") || "").toLowerCase();
    const href = (el.getAttribute("href") || "").toLowerCase();
    const placeholder = (el.getAttribute("placeholder") || "").toLowerCase();
    const text = textOf(el).toLowerCase();
    if (tagName && tag !== tagName) return false;
    if (inputType && elType !== inputType) return false;
    if (needleHref && !href.includes(needleHref)) return false;
    if (needlePlaceholder && !placeholder.includes(needlePlaceholder)) return false;
    if (needleText && !text.includes(needleText)) return false;
    if (needleLabel && !labelText(el).includes(needleLabel)) return false;
    if (!locator.selector && !tagName && !inputType && !needleHref && !needlePlaceholder && !needleText && !needleLabel) {
      return false;
    }
    return true;
  });
  return JSON.stringify({
    elements: matches.slice(0, limit).map((el) => ({
      selector: cssPath(el),
      tag: (el.tagName || "").toLowerCase(),
      text: textOf(el),
      type: el.getAttribute("type") || "",
      href: el.getAttribute("href") || "",
      role: el.getAttribute("role") || "",
      aria_label: el.getAttribute("aria-label") || "",
      aria_checked: el.getAttribute("aria-checked") || ""
    }))
  });
}`, mustJSON(locator), limit)
}

// pierceFindScript returns JS that locates an element by CSS selector across
// the main document, open shadow roots, and same-origin iframes. It is used as
// the fallback for click/type when the plain document.querySelector path
// misses elements that live inside shadow DOM or frames (which the snapshot
// collector now surfaces).
func pierceFindScript(selector string) string {
	return fmt.Sprintf(`() => {
  const selector = %q;
  function findIn(root) {
    if (!root) return null;
    let el = null;
    try { el = root.querySelector(selector); } catch (e) { return null; }
    if (el) return el;
    let all = [];
    try { all = Array.from(root.querySelectorAll("*")); } catch (e) {}
    for (const node of all) {
      if (node.shadowRoot) {
        const hit = findIn(node.shadowRoot);
        if (hit) return hit;
      }
    }
    return null;
  }
  let frames = [];
  try { frames = Array.from(document.querySelectorAll("iframe")); } catch (e) {}
  for (const frame of frames) {
    try {
      if (frame.contentDocument) {
        const hit = findIn(frame.contentDocument);
        if (hit) return true;
      }
    } catch (e) {}
  }
  const el = findIn(document);
  if (!el) return false;
  try { el.scrollIntoView({ block: "center", behavior: "instant" }); } catch (e) {}
  el.click();
  return true;
}`, selector)
}

// pierceTypeScript returns JS that focuses and fills an element found across
// the main document, shadow roots, and same-origin iframes.
func pierceTypeScript(selector, text string) string {
	return fmt.Sprintf(`() => {
  const selector = %q;
  const text = %q;
  function findIn(root) {
    if (!root) return null;
    let el = null;
    try { el = root.querySelector(selector); } catch (e) { return null; }
    if (el) return el;
    let all = [];
    try { all = Array.from(root.querySelectorAll("*")); } catch (e) {}
    for (const node of all) {
      if (node.shadowRoot) {
        const hit = findIn(node.shadowRoot);
        if (hit) return hit;
      }
    }
    return null;
  }
  let frames = [];
  try { frames = Array.from(document.querySelectorAll("iframe")); } catch (e) {}
  for (const frame of frames) {
    try {
      if (frame.contentDocument) {
        const hit = findIn(frame.contentDocument);
        if (hit) return true;
      }
    } catch (e) {}
  }
  const el = findIn(document);
  if (!el) return false;
  try { el.scrollIntoView({ block: "center", behavior: "instant" }); } catch (e) {}
  el.focus();
  if ("value" in el) {
    el.value = text;
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
    return true;
  }
  if (el.isContentEditable) {
    el.textContent = text;
    el.dispatchEvent(new Event("input", { bubbles: true }));
    return true;
  }
  return false;
}`, selector, text)
}

func searchInputScript() string {
	return `() => {
  function cssPath(el) {
    if (!(el instanceof Element)) return "";
    if (el.id) return "#" + CSS.escape(el.id);
    const path = [];
    let current = el;
    while (current && current.nodeType === Node.ELEMENT_NODE && current !== document.body) {
      let selector = current.nodeName.toLowerCase();
      const siblings = current.parentNode ? Array.from(current.parentNode.children).filter((node) => node.nodeName === current.nodeName) : [];
      if (siblings.length > 1) {
        selector += ":nth-of-type(" + (siblings.indexOf(current) + 1) + ")";
      }
      path.unshift(selector);
      current = current.parentElement;
    }
    return path.join(" > ");
  }
  function visible(el) {
    const rect = el.getBoundingClientRect();
    const style = window.getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
  }
  const selectors = [
    'input[type="search"]',
    'input[name*="search" i]',
    'input[name="q" i]',
    'input[placeholder*="search" i]',
    'input[placeholder*="搜" i]',
    '[role="searchbox"]',
    'form input[type="text"]',
    'input[type="text"]'
  ];
  for (const selector of selectors) {
    const el = Array.from(document.querySelectorAll(selector)).find((node) => visible(node));
    if (el) {
      return JSON.stringify({ selector: cssPath(el) });
    }
  }
  return JSON.stringify({ error: "no visible search input found" });
}`
}

func networkSnapshotScript(maxEntries int) string {
	if maxEntries <= 0 {
		maxEntries = 40
	}
	return fmt.Sprintf(`() => {
  const entries = performance.getEntriesByType("navigation").concat(performance.getEntriesByType("resource"));
  return JSON.stringify({
    url: location.href,
    entries: entries.slice(0, %d).map((entry) => ({
      entry_type: entry.entryType || "",
      url: entry.name || "",
      initiator_type: entry.initiatorType || "",
      transfer_size: entry.transferSize || 0,
      duration_ms: entry.duration || 0,
      start_time_ms: entry.startTime || 0
    }))
  });
}`, maxEntries)
}

func resolveBrowserUploadPaths(workspace, single string, many []string) ([]string, error) {
	candidates := make([]string, 0, len(many)+1)
	if strings.TrimSpace(single) != "" {
		candidates = append(candidates, single)
	}
	candidates = append(candidates, many...)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("upload_file requires path or paths")
	}
	paths := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	root, err := workspacefs.New(workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, raw := range candidates {
		absPath, err := root.Abs(raw)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[absPath]; ok {
			continue
		}
		info, err := root.Stat(raw)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			return nil, fmt.Errorf("upload_file path is a directory: %s", raw)
		}
		seen[absPath] = struct{}{}
		paths = append(paths, absPath)
	}
	return paths, nil
}
