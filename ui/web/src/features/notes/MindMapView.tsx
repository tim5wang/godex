import { useEffect, useRef } from "react";
import MindElixir, { generateUUID, type MindElixirData, type NodeObj } from "mind-elixir";
import "mind-elixir/style";

export interface MindMapViewProps {
  /** 笔记标题，用作脑图根节点 */
  title: string;
  /** Markdown 内容（挂载时解析一次；脑图内编辑后通过 onChange 回写） */
  content: string;
  /** 脑图编辑后回写为 Markdown */
  onChange: (markdown: string) => void;
}

/** 标题层级上限：Markdown 仅支持 1-6 级标题，更深层级回写为缩进列表 */
const MAX_HEADING_DEPTH = 6;

/** 将行内 Markdown 格式简化为纯文本（仅影响脑图节点显示/回写） */
export function stripInlineMarkdown(text: string): string {
  return text
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\*\*([^*]+)\*\*/g, "$1")
    .replace(/\*([^*]+)\*/g, "$1")
    .replace(/__([^_]+)__/g, "$1")
    .replace(/_([^_]+)_/g, "$1")
    .replace(/`([^`]+)`/g, "$1")
    .replace(/~~([^~]+)~~/g, "$1")
    .trim();
}

/** 节点来源元数据：heading 记录 markdown 标题层级，list 记录列表项 */
export interface NodeMeta {
  kind?: "heading" | "list";
  /** markdown 标题层级（1-6），仅 heading 节点使用 */
  mdLevel?: number;
}

function makeNode(topic: string, meta?: NodeMeta): NodeObj<NodeMeta> {
  return { topic, id: generateUUID(), expanded: true, metadata: meta };
}

/**
 * Markdown → 脑图树。
 *
 * 转换约定：
 * - 根节点 = 笔记标题；
 * - `#`~`######` 标题 → 对应层级（mdLevel）的节点；
 * - `-`/`*`/`+`/`1.` 列表项 → 节点，2 空格缩进 = 1 级（挂到最近标题或上一列表项下）；
 * - 代码块、表格、段落等非标题/列表内容不映射（脑图只表达层级结构）。
 */
export function buildMindData(title: string, markdown: string): MindElixirData<NodeMeta> {
  const root = makeNode(stripInlineMarkdown(title) || "Untitled");
  const headingStack: (NodeObj<NodeMeta> | undefined)[] = [root];
  const listStack: NodeObj<NodeMeta>[] = [];

  const headingRe = /^(#{1,})\s+(.*)$/;
  const listRe = /^(\s*)([-*+]|\d+\.)\s+(.*)$/;

  for (const rawLine of markdown.split("\n")) {
    const line = rawLine.replace(/\r$/, "");
    if (!line.trim()) {
      continue;
    }

    const heading = line.match(headingRe);
    if (heading) {
      const hashCount = heading[1].length;
      if (hashCount > MAX_HEADING_DEPTH) {
        // 超 6 级标题折叠为列表项，挂到最近标题下
        const node = makeNode(stripInlineMarkdown(heading[2]), { kind: "list" });
        let parent: NodeObj<NodeMeta> | undefined;
        for (let d = headingStack.length - 1; d >= 1; d--) {
          if (headingStack[d]) {
            parent = headingStack[d]!;
            break;
          }
        }
        (parent ?? root).children ??= [];
        (parent ?? root).children!.push(node);
        listStack.length = 0;
        listStack[0] = node;
        continue;
      }
      const depth = hashCount;
      const node = makeNode(stripInlineMarkdown(heading[2]), { kind: "heading", mdLevel: depth });
      let parent: NodeObj<NodeMeta> = root;
      for (let d = depth - 1; d >= 1; d--) {
        if (headingStack[d]) {
          parent = headingStack[d]!;
          break;
        }
      }
      (parent.children ??= []).push(node);
      headingStack.length = depth;
      headingStack[depth] = node;
      listStack.length = 0;
      continue;
    }

    const list = line.match(listRe);
    if (list) {
      const indentLevel = Math.floor(list[1].replace(/\t/g, "  ").length / 2);
      const node = makeNode(stripInlineMarkdown(list[3]), { kind: "list" });
      let parent: NodeObj<NodeMeta> | undefined = indentLevel > 0 ? listStack[indentLevel - 1] : undefined;
      if (!parent) {
        for (let d = headingStack.length - 1; d >= 1; d--) {
          if (headingStack[d]) {
            parent = headingStack[d]!;
            break;
          }
        }
      }
      if (!parent) {
        parent = root;
      }
      (parent.children ??= []).push(node);
      listStack.length = indentLevel;
      listStack[indentLevel] = node;
      continue;
    }

    // 其余行（段落/代码块/表格等）不映射到脑图
  }

  return { nodeData: root, direction: MindElixir.SIDE };
}

/**
 * 脑图树 → Markdown。
 *
 * 转换约定：
 * - 根节点（= 笔记标题）不写回正文；
 * - heading 节点 → `#`×mdLevel 标题（脑图内新建的无 metadata 节点按树深度推导标题层级）；
 * - list 节点 → `-` 无序列表，缩进 = 自最近 heading 祖先以来的连续列表层级 × 2 空格；
 * - 行内格式已在解析时简化为纯文本，回写后不再保留。
 */
export function buildMarkdown(nodeData: NodeObj<NodeMeta>): string {
  const lines: string[] = [];
  const walk = (node: NodeObj<NodeMeta>, treeDepth: number, listDepth: number) => {
    const isRoot = treeDepth === 0;
    if (!isRoot) {
      if (node.metadata?.kind === "list") {
        lines.push(`${"  ".repeat(Math.max(0, listDepth))}- ${node.topic}`);
      } else {
        const level = node.metadata?.mdLevel ?? Math.min(treeDepth, MAX_HEADING_DEPTH);
        lines.push(`${"#".repeat(level)} ${node.topic}`);
      }
    }
    const parentIsList = !isRoot && node.metadata?.kind === "list";
    for (const child of node.children ?? []) {
      const childIsList = child.metadata?.kind === "list";
      const childListDepth = childIsList ? (parentIsList ? listDepth + 1 : 0) : 0;
      walk(child, treeDepth + 1, childListDepth);
    }
  };
  walk(nodeData, 0, 0);
  return lines.length > 0 ? `${lines.join("\n")}\n` : "";
}

export function MindMapView({ title, content, onChange }: MindMapViewProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;

  // 只在首次挂载时解析一次 markdown，避免脑图编辑回写导致循环重建
  const initialRef = useRef<{ title: string; content: string } | null>(null);
  if (!initialRef.current) {
    initialRef.current = { title, content };
  }

  useEffect(() => {
    const el = containerRef.current;
    if (!el) {
      return;
    }
    const initial = initialRef.current!;
    let mind: MindElixir<NodeMeta> | undefined;
    let syncTimer: number | undefined;
    let destroyed = false;

    const data = buildMindData(initial.title, initial.content);
    mind = new MindElixir<NodeMeta>({
      el,
      direction: MindElixir.SIDE,
      editable: true,
      contextMenu: true,
      toolBar: false,
      keypress: true,
      allowUndo: true,
      theme: MindElixir.THEME,
    });
    void mind.init(data).then((err) => {
      if (destroyed || err || !mind) {
        return;
      }
      // 脑图内任何结构/文字编辑 → 防抖导出 markdown 回写
      mind.bus.addListener("operation", () => {
        window.clearTimeout(syncTimer);
        syncTimer = window.setTimeout(() => {
          const snapshot = mind?.getData();
          if (snapshot) {
            onChangeRef.current(buildMarkdown(snapshot.nodeData));
          }
        }, 400);
      });
    });

    return () => {
      destroyed = true;
      window.clearTimeout(syncTimer);
      mind?.destroy();
    };
  }, []);

  return <div className="notes-mindmap" ref={containerRef} />;
}
