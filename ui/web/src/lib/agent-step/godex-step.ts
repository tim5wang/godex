/**
 * <godex-step> — Agent Step Platform URL-embed component (Phase C).
 *
 * A self-contained Web Component that wraps the Phase B SDK (StepClient) so a
 * business page gets an agent interaction block with a single tag:
 *
 * ```html
 * <script src="godex-step.js"></script>
 * <godex-step
 *   base-url="https://godex.claw.carc.top"
 *   api-key="biz_xxx"
 *   prompt="分析订单"
 * ></godex-step>
 * ```
 *
 * Renders with native DOM + Shadow DOM (no React/antd dependency): a prompt
 * input, a run button, the streaming result text, any structured output, and
 * ui_card form/button/card blocks. ui_card form submissions are sent back to
 * the running session as follow-up messages via the SDK stream endpoint.
 */
import { StepClient, type RuntimeStepEvent, type StepRequest, type UiCardData } from "./client";

const STYLES = `
:host {
  display: block;
  font-family: system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
  font-size: 14px;
  color: #1f2328;
  box-sizing: border-box;
}
* { box-sizing: border-box; }
.wrap { border: 1px solid #d0d7de; border-radius: 8px; padding: 14px; background: #fff; }
.row { display: flex; gap: 8px; margin-bottom: 10px; }
input[type="text"] {
  flex: 1; padding: 7px 10px; border: 1px solid #d0d7de; border-radius: 6px; font-size: 14px;
}
input[type="text"]:focus { outline: none; border-color: #0969da; }
button {
  padding: 7px 16px; border: none; border-radius: 6px; cursor: pointer; font-size: 14px;
  background: #0969da; color: #fff; white-space: nowrap;
}
button:hover { background: #0860ca; }
button:disabled { background: #8bb3e8; cursor: default; }
.result { white-space: pre-wrap; line-height: 1.55; }
.meta { color: #57606a; font-size: 12px; margin-top: 8px; }
.error { color: #cf222e; background: #ffebe9; border: 1px solid #ffcecb; border-radius: 6px; padding: 8px 10px; }
.card { border: 1px solid #d0d7de; border-radius: 6px; padding: 12px; margin-top: 10px; background: #f6f8fa; }
.card h4 { margin: 0 0 8px; font-size: 14px; }
.card p { margin: 0 0 8px; color: #57606a; }
.card .btn-row { display: flex; flex-wrap: wrap; gap: 8px; }
.card button.ghost { background: #fff; color: #0969da; border: 1px solid #d0d7de; }
.card form { display: flex; flex-direction: column; gap: 8px; }
.card form label { font-size: 13px; color: #57606a; }
.card form input, .card form select, .card form textarea {
  padding: 6px 8px; border: 1px solid #d0d7de; border-radius: 6px; font-size: 14px;
  width: 100%;
}
.status { display: inline-block; font-size: 12px; color: #57606a; margin-bottom: 8px; }
.status.running { color: #0969da; }
.status.done { color: #1a7f37; }
`;

/**
 * <godex-step> custom element.
 *
 * Attributes:
 *  - base-url  (required unless the page is served by godex itself)
 *  - api-key   (required; biz_... business system key)
 *  - prompt    (optional initial prompt in the input)
 *  - placeholder (optional input placeholder)
 */
export class GodexStepElement extends HTMLElement {
  static observedAttributes = ["base-url", "api-key", "prompt", "placeholder"];

  private client: StepClient | null = null;
  private input: HTMLInputElement;
  private runBtn: HTMLButtonElement;
  private status: HTMLSpanElement;
  private result: HTMLDivElement;
  private cards: HTMLDivElement;
  private meta: HTMLDivElement;
  private controller: AbortController | null = null;
  private currentStepId = "";
  private currentSessionId = "";

  constructor() {
    super();
    const shadow = this.attachShadow({ mode: "open" });
    const style = document.createElement("style");
    style.textContent = STYLES;

    const wrap = document.createElement("div");
    wrap.className = "wrap";

    const row = document.createElement("div");
    row.className = "row";
    this.input = document.createElement("input");
    this.input.type = "text";
    this.input.placeholder = this.getAttribute("placeholder") || "输入你的业务指令…";
    this.runBtn = document.createElement("button");
    this.runBtn.textContent = "运行";
    row.append(this.input, this.runBtn);
    wrap.append(row);

    this.status = document.createElement("span");
    this.status.className = "status";
    wrap.append(this.status);

    this.result = document.createElement("div");
    this.result.className = "result";
    wrap.append(this.result);

    this.cards = document.createElement("div");
    wrap.append(this.cards);

    this.meta = document.createElement("div");
    this.meta.className = "meta";
    wrap.append(this.meta);

    shadow.append(style, wrap);

    this.runBtn.addEventListener("click", () => this.run());
    this.input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        this.run();
      }
    });
  }

  connectedCallback() {
    const prompt = this.getAttribute("prompt");
    if (prompt) {
      this.input.value = prompt;
    }
  }

  private resolveClient(): StepClient | null {
    const baseUrl = this.getAttribute("base-url");
    const apiKey = this.getAttribute("api-key");
    if (!baseUrl || !apiKey) {
      this.renderError("需要 base-url 与 api-key 属性");
      return null;
    }
    this.client ??= new StepClient({ baseUrl, apiKey });
    return this.client;
  }

  private makeStepId(): string {
    return `stp_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`;
  }

  /**
   * Subscribe to the step session's live events and append assistant text
   * deltas to the result box as they stream in. Subscribe before POSTing so no
   * delta is missed (replay=active only replays the current active turn, which
   * is empty before the run starts). Returns the stream controller to abort.
   */
  private async openStream(stepId: string): Promise<AbortController> {
    const client = this.resolveClient();
    if (!client) {
      return new AbortController();
    }
    return client.streamEvents(
      stepId,
      (event) => {
        if (event.type === "assistant_text_delta") {
          const text = (event.payload as { text?: unknown } | undefined)?.text;
          if (typeof text === "string") {
            this.result.textContent += text;
          }
        }
        if (event.type === "tool_call_finished") {
          const payload = event.payload as { name?: unknown; output?: unknown } | undefined;
          // The ui_card tool echoes its structured card as JSON in the tool
          // output; render it as an interactive card (button group / form).
          if (payload?.name === "ui_card" && typeof payload.output === "string") {
            try {
              const card = JSON.parse(payload.output) as UiCardData;
              if (card && typeof card === "object") {
                this.renderCard(card);
              }
            } catch {
              // ignore malformed card payloads
            }
          }
        }
      },
      this.controller?.signal,
    );
  }

  private async run() {
    const client = this.resolveClient();
    const prompt = this.input.value.trim();
    if (!client || !prompt) {
      return;
    }
    this.controller?.abort();
    this.controller = new AbortController();
    this.runBtn.disabled = true;
    this.status.textContent = "运行中…";
    this.status.className = "status running";
    this.result.textContent = "";
    this.cards.replaceChildren();
    this.meta.textContent = "";
    this.setAttribute("prompt", prompt);

    // Multi-turn: keep the same step_id across runs so the server's
    // deterministic session is reused (same conversation), and pass the
    // session_id back so the next turn continues it.
    if (!this.currentStepId) {
      this.currentStepId = this.makeStepId();
    }

    const req: StepRequest = {
      step_id: this.currentStepId,
      session_id: this.currentSessionId || undefined,
      prompt,
      inputs: this.parseInputs(this.getAttribute("inputs")),
      context: this.parseContext(this.getAttribute("context")),
      tools: this.parseTools(this.getAttribute("tools")),
    };

    try {
      // Subscribe before POST so assistant deltas stream in live.
      const stream = await this.openStream(this.currentStepId);
      try {
        const result = await client.createStep(req, this.controller.signal);
        this.currentStepId = result.step_id;
        this.currentSessionId = result.session_id;
        this.status.textContent = "完成";
        this.status.className = "status done";
        this.result.textContent = result.text || this.result.textContent;
        this.meta.textContent = `step ${result.step_id} · session ${result.session_id}`;
        if (result.output !== undefined) {
          this.renderStructured(result.output);
        }
      } finally {
        stream.abort();
      }
    } catch (err) {
      if ((err as Error).name === "AbortError") {
        this.status.textContent = "已中止";
        return;
      }
      this.renderError((err as Error).message);
    } finally {
      this.runBtn.disabled = false;
    }
  }

  // ui_card interaction loop: the value the user submitted through a card is
  // injected back into the SAME step session (replyStep), then we poll the
  // terminal state so the agent's continuation renders in place.
  private async submitCardValue(value: unknown) {
    const client = this.resolveClient();
    if (!client || !this.currentStepId) {
      return;
    }
    this.controller?.abort();
    this.controller = new AbortController();
    this.runBtn.disabled = true;
    this.status.textContent = "处理中…";
    this.status.className = "status running";
    this.result.textContent = "";
    this.cards.replaceChildren();

    try {
      const stream = await this.openStream(this.currentStepId);
      try {
        await client.replyStep(this.currentStepId, value, { signal: this.controller.signal });
        // The reply is async: poll until the step leaves the running state.
        const result = await client.getStep(this.currentStepId, this.controller.signal);
        this.currentStepId = result.step_id;
        this.currentSessionId = result.session_id || this.currentSessionId;
        this.status.textContent = "完成";
        this.status.className = "status done";
        this.result.textContent = result.text || this.result.textContent;
        this.meta.textContent = `step ${result.step_id} · session ${result.session_id}`;
        if (result.output !== undefined) {
          this.renderStructured(result.output);
        }
      } finally {
        stream.abort();
      }
    } catch (err) {
      if ((err as Error).name === "AbortError") {
        this.status.textContent = "已中止";
        return;
      }
      this.renderError((err as Error).message);
    } finally {
      this.runBtn.disabled = false;
    }
  }

  // --- rendering helpers (native DOM) ---

  private renderError(message: string) {
    const box = document.createElement("div");
    box.className = "error";
    box.textContent = message;
    this.result.replaceChildren(box);
    this.status.textContent = "失败";
    this.status.className = "status";
  }

  private renderStructured(output: unknown) {
    const pre = document.createElement("pre");
    pre.style.cssText = "background:#f6f8fa;border:1px solid #d0d7de;border-radius:6px;padding:10px;overflow:auto;margin-top:8px;";
    pre.textContent = JSON.stringify(output, null, 2);
    this.cards.append(pre);
  }

  private renderCard(card: UiCardData) {
    const host = document.createElement("div");
    host.className = "card";

    if (card.title) {
      const h = document.createElement("h4");
      h.textContent = card.title;
      host.append(h);
    }
    if (card.content) {
      const p = document.createElement("p");
      p.textContent = card.content;
      host.append(p);
    }

    if (card.kind === "button_group" || (card.actions?.length && !card.fields?.length)) {
      const row = document.createElement("div");
      row.className = "btn-row";
      for (const action of card.actions ?? []) {
        const b = document.createElement("button");
        b.className = "ghost";
        b.textContent = action.label;
        b.addEventListener("click", () => this.submitCardValue(action.value ?? action.label));
        row.append(b);
      }
      host.append(row);
    } else if (card.fields?.length) {
      const form = document.createElement("form");
      for (const field of card.fields) {
        const label = document.createElement("label");
        label.textContent = field.label || field.name;
        let control: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;
        if (field.type === "select") {
          control = document.createElement("select");
          for (const opt of field.options ?? []) {
            const o = document.createElement("option");
            o.value = opt.value;
            o.textContent = opt.label;
            control.append(o);
          }
        } else if (field.type === "textarea") {
          control = document.createElement("textarea");
          control.rows = 3;
        } else {
          control = document.createElement("input");
          if (field.type === "number") {
            control.type = "number";
          }
        }
        control.name = field.name;
        if (field.placeholder && !(control instanceof HTMLSelectElement)) {
          control.placeholder = field.placeholder;
        }
        label.append(control);
        form.append(label);
      }
      const submit = document.createElement("button");
      submit.type = "submit";
      submit.textContent = "提交";
      form.append(submit);
      form.addEventListener("submit", (e) => {
        e.preventDefault();
        const data = Object.fromEntries(new FormData(form).entries());
        this.submitCardValue(data);
      });
      host.append(form);
    } else {
      // plain card: content already rendered above
    }
    this.cards.append(host);
  }

  private parseInputs(raw: string | null): Record<string, unknown> | undefined {
    if (!raw) {
      return undefined;
    }
    try {
      return JSON.parse(raw) as Record<string, unknown>;
    } catch {
      return undefined;
    }
  }

  private parseContext(raw: string | null): { recall?: string[] } | undefined {
    if (!raw) {
      return undefined;
    }
    try {
      return JSON.parse(raw) as { recall?: string[] };
    } catch {
      return undefined;
    }
  }

  private parseTools(raw: string | null): { mcp?: string[]; sandbox?: string[] } | undefined {
    if (!raw) {
      return undefined;
    }
    try {
      return JSON.parse(raw) as { mcp?: string[]; sandbox?: string[] };
    } catch {
      return undefined;
    }
  }
}

// Register once (guard against double inclusion).
if (!customElements.get("godex-step")) {
  customElements.define("godex-step", GodexStepElement);
}

export default GodexStepElement;
