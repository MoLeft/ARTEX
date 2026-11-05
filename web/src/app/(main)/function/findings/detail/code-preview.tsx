"use client";

import * as React from "react";

import { defaultHighlightStyle, LanguageDescription, syntaxHighlighting } from "@codemirror/language";
import { languages } from "@codemirror/language-data";
import { EditorState } from "@codemirror/state";
import { EditorView, lineNumbers } from "@codemirror/view";

const theme = EditorView.theme({
  "&": { height: "100%", backgroundColor: "transparent", color: "var(--foreground)" },
  ".cm-scroller": { overflow: "auto", fontFamily: "var(--font-mono)", fontSize: "0.75rem", lineHeight: "1.5rem" },
  ".cm-content": { padding: "0.75rem" },
  ".cm-gutters": {
    backgroundColor: "var(--muted)",
    color: "var(--muted-foreground)",
    border: "0",
    borderRight: "1px solid var(--border)",
  },
});

export default function CodePreview({ path, content }: { path: string; content: string }) {
  const host = React.useRef<HTMLDivElement>(null);

  React.useEffect(() => {
    let active = true;
    let view: EditorView | undefined;
    const filename = path.split("/").pop() ?? path;
    const language = LanguageDescription.matchFilename(languages, filename);
    (language?.load() ?? Promise.resolve(null))
      .catch(() => null)
      .then((support) => {
        if (!active || !host.current) return;
        view = new EditorView({
          parent: host.current,
          state: EditorState.create({
            doc: content,
            extensions: [
              lineNumbers(),
              theme,
              syntaxHighlighting(defaultHighlightStyle),
              EditorView.editable.of(false),
              EditorState.readOnly.of(true),
              EditorView.contentAttributes.of({
                role: "textbox",
                "aria-label": `只读文件 ${filename}`,
                "aria-readonly": "true",
              }),
              ...(support ? [support] : []),
            ],
          }),
        });
      });
    return () => {
      active = false;
      view?.destroy();
    };
  }, [content, path]);

  return <div ref={host} className="h-full min-h-[50vh] overflow-hidden rounded border" />;
}
