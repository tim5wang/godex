import ReactMarkdown from "react-markdown";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";
import rehypeRaw from "rehype-raw";
import rehypeSanitize, { defaultSchema } from "rehype-sanitize";
import { DiagramBlock, type DiagramLanguage } from "./DiagramBlock";

interface MarkdownRendererProps {
  content: string;
}

const DIAGRAM_LANG: Record<string, DiagramLanguage> = {
  mermaid: "mermaid",
  plantuml: "plantuml",
  uml: "plantuml",
};

/**
 * Markdown renderer with:
 *   - safe raw-HTML support (rehype-raw + rehype-sanitize GitHub schema)
 *   - fenced code blocks rendered as diagrams for ```mermaid / ```plantuml
 */
export function MarkdownRenderer({ content }: MarkdownRendererProps) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm, remarkBreaks]}
      rehypePlugins={[rehypeRaw, [rehypeSanitize, defaultSchema]]}
      components={{
        a: ({ node: _node, ...props }) => (
          <a
            {...props}
            target="_blank"
            rel="noreferrer"
          />
        ),
        code: ({ node: _node, className, children, ...props }) => {
          const lang = extractDiagramLanguage(className);
          if (lang) {
            return <DiagramBlock language={lang} code={String(children ?? "").replace(/\n$/, "")} />;
          }
          return (
            <code className={className} {...props}>
              {children}
            </code>
          );
        },
      }}
    >
      {content}
    </ReactMarkdown>
  );
}

/** Detect ```mermaid / ```plantuml / ```uml fenced blocks (className="language-mermaid"). */
function extractDiagramLanguage(className?: string): DiagramLanguage | null {
  if (typeof className !== "string") {
    return null;
  }
  const match = className.match(/language-([a-z]+)/);
  if (!match) {
    return null;
  }
  return DIAGRAM_LANG[match[1].toLowerCase()] ?? null;
}
