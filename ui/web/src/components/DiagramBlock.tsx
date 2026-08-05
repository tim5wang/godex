import { useEffect, useState } from "react";
import { Alert, Spin } from "antd";

export type DiagramLanguage = "mermaid" | "plantuml";

interface DiagramBlockProps {
  language: DiagramLanguage;
  code: string;
}

/**
 * Renders a fenced code block as a diagram:
 *
 *   - mermaid: lazy-imports mermaid and renders the source to inline SVG.
 *     `securityLevel: "strict"` strips script/HTML so the SVG can be injected
 *     safely. Falls back to the raw code + error message on failure.
 *   - plantuml: lazy-imports plantuml-encoder (tiny deflate encoder) and
 *     renders an <img> from the PlantUML server. Falls back to raw code if
 *     the server image cannot be loaded.
 *
 * Both are lazy-loaded so they never affect the main bundle — a chunk is only
 * fetched when a message actually contains a diagram.
 */
export function DiagramBlock({ language, code }: DiagramBlockProps) {
  if (language === "mermaid") {
    return <MermaidDiagram code={code} />;
  }
  return <PlantUmlDiagram code={code} />;
}

// ---------------------------------------------------------------------------
// mermaid
// ---------------------------------------------------------------------------

let diagramCounter = 0;
function nextDiagramId() {
  diagramCounter += 1;
  return `godex-diagram-${diagramCounter}`;
}

function MermaidDiagram({ code }: { code: string }) {
  const [svg, setSvg] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setSvg(null);
    setError(null);
    (async () => {
      try {
        const mermaid = (await import("mermaid")).default;
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: "strict",
          theme: "default",
          fontFamily: "inherit",
        });
        const id = nextDiagramId();
        const { svg: rendered } = await mermaid.render(id, code);
        if (!cancelled) setSvg(rendered);
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [code]);

  if (error) {
    return <DiagramError language="mermaid" message={error} code={code} />;
  }
  if (!svg) {
    return <DiagramLoading />;
  }
  return <div className="diagram-block diagram-block-mermaid" dangerouslySetInnerHTML={{ __html: svg }} />;
}

// ---------------------------------------------------------------------------
// plantuml
// ---------------------------------------------------------------------------

const PLANTUML_SERVER = "https://www.plantuml.com/plantuml/svg/";

function PlantUmlDiagram({ code }: { code: string }) {
  const [src, setSrc] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [imgFailed, setImgFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setSrc(null);
    setError(null);
    setImgFailed(false);
    (async () => {
      try {
        const { encode } = await import("plantuml-encoder");
        const encoded = encode(normalizePlantuml(code));
        if (!cancelled) setSrc(`${PLANTUML_SERVER}${encoded}`);
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [code]);

  if (error || imgFailed) {
    return <DiagramError language="plantuml" message={error ?? "Failed to load PlantUML image (network or server issue)."} code={code} />;
  }
  if (!src) {
    return <DiagramLoading />;
  }
  return (
    <div className="diagram-block diagram-block-plantuml">
      <img src={src} alt="PlantUML diagram" onError={() => setImgFailed(true)} />
      <div className="diagram-block-actions">
        <a href={src} target="_blank" rel="noreferrer">
          Open diagram ↗
        </a>
      </div>
    </div>
  );
}

/** plantuml-encoder expects a full @startuml…@enduml document. */
function normalizePlantuml(code: string): string {
  const trimmed = code.trim();
  if (trimmed.startsWith("@startuml")) {
    return trimmed;
  }
  return `@startuml\n${trimmed}\n@enduml`;
}

// ---------------------------------------------------------------------------
// shared fallbacks
// ---------------------------------------------------------------------------

function DiagramLoading() {
  return (
    <div className="diagram-block diagram-block-loading">
      <Spin size="small" />
      <span>Rendering diagram…</span>
    </div>
  );
}

function DiagramError({ language, message, code }: { language: DiagramLanguage; message: string; code: string }) {
  return (
    <div className="diagram-block diagram-block-error">
      <Alert type="warning" showIcon message={`${language} render failed`} description={message} />
      <pre className="diagram-block-code">{code}</pre>
    </div>
  );
}
