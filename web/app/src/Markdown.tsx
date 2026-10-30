import { Fragment, ReactNode } from "react";

type Block =
  | { kind: "code"; lang: string; text: string }
  | { kind: "paragraph"; text: string }
  | { kind: "heading"; level: number; text: string }
  | { kind: "quote"; text: string }
  | { kind: "list"; ordered: boolean; items: string[] };

function parseBlocks(source: string): Block[] {
  const lines = source.replace(/\r\n/g, "\n").split("\n");
  const blocks: Block[] = [];
  let paragraph: string[] = [];
  const flush = () => {
    if (paragraph.length) {
      blocks.push({ kind: "paragraph", text: paragraph.join("\n") });
      paragraph = [];
    }
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const fence = line.match(/^```(.*)$/);
    if (fence) {
      flush();
      const body: string[] = [];
      i++;
      while (i < lines.length && !/^```/.test(lines[i])) body.push(lines[i++]);
      blocks.push({ kind: "code", lang: fence[1].trim(), text: body.join("\n") });
      continue;
    }
    if (!line.trim()) {
      flush();
      continue;
    }
    const heading = line.match(/^(#{1,4})\s+(.*)$/);
    if (heading) {
      flush();
      blocks.push({ kind: "heading", level: heading[1].length, text: heading[2] });
      continue;
    }
    const quote = line.match(/^>\s?(.*)$/);
    if (quote) {
      flush();
      blocks.push({ kind: "quote", text: quote[1] });
      continue;
    }
    if (/^[-*]\s+/.test(line) || /^\d+\.\s+/.test(line)) {
      flush();
      const ordered = /^\d+\./.test(line);
      const items: string[] = [];
      while (i < lines.length && (/^[-*]\s+/.test(lines[i]) || /^\d+\.\s+/.test(lines[i]))) {
        items.push(lines[i].replace(/^([-*]|\d+\.)\s+/, ""));
        i++;
      }
      i--;
      blocks.push({ kind: "list", ordered, items });
      continue;
    }
    paragraph.push(line);
  }
  flush();
  return blocks;
}

function safeURL(raw: string): string | null {
  try {
    const url = new URL(raw, window.location.origin);
    if (["http:", "https:", "mailto:"].includes(url.protocol)) return url.href;
  } catch {
    return null;
  }
  return null;
}

function inline(text: string, keyPrefix: string): ReactNode[] {
  const out: ReactNode[] = [];
  const pattern = /(`[^`]+`|\*\*[^*]+\*\*|\*[^*]+\*|\[[^\]]+\]\([^)]+\)|@[A-Za-z0-9_.-]+)/g;
  let last = 0;
  let match: RegExpExecArray | null;
  let index = 0;
  while ((match = pattern.exec(text)) !== null) {
    if (match.index > last) out.push(text.slice(last, match.index));
    const token = match[0];
    const key = `${keyPrefix}-${index++}`;
    if (token.startsWith("`")) {
      out.push(<code key={key}>{token.slice(1, -1)}</code>);
    } else if (token.startsWith("**")) {
      out.push(<strong key={key}>{token.slice(2, -2)}</strong>);
    } else if (token.startsWith("*")) {
      out.push(<em key={key}>{token.slice(1, -1)}</em>);
    } else if (token.startsWith("@")) {
      out.push(
        <span className="mention" key={key}>
          {token}
        </span>,
      );
    } else {
      const link = token.match(/^\[([^\]]+)\]\(([^)]+)\)$/);
      const href = link ? safeURL(link[2]) : null;
      if (link && href) {
        out.push(
          <a key={key} href={href} target="_blank" rel="noreferrer noopener">
            {link[1]}
          </a>,
        );
      } else {
        out.push(token);
      }
    }
    last = pattern.lastIndex;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

export function Markdown({ text }: { text: string }) {
  const blocks = parseBlocks(text);
  return (
    <div className="markdown">
      {blocks.map((block, index) => {
        const key = `b-${index}`;
        switch (block.kind) {
          case "code":
            return (
              <pre key={key} className="code-block">
                {block.lang && <span className="code-lang">{block.lang}</span>}
                <code>{block.text}</code>
              </pre>
            );
          case "heading": {
            const Tag = (`h${Math.min(block.level + 2, 6)}`) as "h3" | "h4" | "h5" | "h6";
            return <Tag key={key}>{inline(block.text, key)}</Tag>;
          }
          case "quote":
            return <blockquote key={key}>{inline(block.text, key)}</blockquote>;
          case "list": {
            const Tag = block.ordered ? "ol" : "ul";
            return (
              <Tag key={key}>
                {block.items.map((item, itemIndex) => (
                  <li key={`${key}-${itemIndex}`}>{inline(item, `${key}-${itemIndex}`)}</li>
                ))}
              </Tag>
            );
          }
          default:
            return (
              <p key={key}>
                {block.text.split("\n").map((line, lineIndex) => (
                  <Fragment key={`${key}-${lineIndex}`}>
                    {lineIndex > 0 && <br />}
                    {inline(line, `${key}-${lineIndex}`)}
                  </Fragment>
                ))}
              </p>
            );
        }
      })}
    </div>
  );
}
