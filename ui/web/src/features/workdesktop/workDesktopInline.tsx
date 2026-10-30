import { useEffect, useRef, useState, type ReactNode } from "react";
import { Input, Select, Spin } from "antd";

/**
 * InlineEdit is the shared in-place editing primitive for the work desktop.
 *
 * The desktop used to edit every record through a modal form, which breaks the
 * working context (the user loses sight of the stream while the dialog is open).
 * This shows the value read-only until it is clicked, then swaps in an input:
 * Enter saves, Esc cancels, blur saves. It is deliberately uncontrolled while
 * editing so a parent re-render (e.g. a background refetch) cannot clobber what
 * the user is typing.
 */
export function InlineEdit(props: {
  value: string;
  ariaLabel: string;
  placeholder?: string;
  multiline?: boolean;
  disabled?: boolean;
  className?: string;
  /** Display renderer; defaults to the raw value. */
  render?: (value: string) => ReactNode;
  onSave: (next: string) => Promise<unknown> | unknown;
  /** When true, an empty value cannot be saved (reverts instead). */
  required?: boolean;
}) {
  const { value, ariaLabel, placeholder, multiline, disabled, className, render, onSave, required } = props;
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(value);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const inputRef = useRef<HTMLInputElement | HTMLTextAreaElement | null>(null);

  // Re-sync the draft when the value changes while NOT editing (e.g. refetch after
  // a save), but never while the user is typing.
  useEffect(() => {
    if (!editing) setDraft(value);
  }, [value, editing]);

  useEffect(() => {
    if (editing) inputRef.current?.focus();
  }, [editing]);

  const commit = async () => {
    const next = draft.trim();
    if (required && !next) {
      setEditing(false);
      setDraft(value);
      return;
    }
    if (next === value.trim()) {
      setEditing(false);
      return;
    }
    setSaving(true);
    setError("");
    try {
      await onSave(next);
      setEditing(false);
    } catch (err) {
      // Keep the editor open on failure so the user can retry or fix the value.
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const cancel = () => {
    setEditing(false);
    setDraft(value);
    setError("");
  };

  if (!editing) {
    return (
      <span className={`wd-inline ${className ?? ""}`}>
        <button
          type="button"
          className="wd-inline-value"
          aria-label={ariaLabel}
          disabled={disabled}
          onClick={() => !disabled && setEditing(true)}
        >
          {render ? render(value) : (value || <span className="wd-muted">{placeholder}</span>)}
        </button>
      </span>
    );
  }

  const onKeyDown = (event: React.KeyboardEvent) => {
    if (event.key === "Escape") {
      event.preventDefault();
      cancel();
      return;
    }
    // Enter saves; a multiline field needs the modifier so newlines still work.
    if (event.key === "Enter" && (!multiline || event.metaKey || event.ctrlKey)) {
      event.preventDefault();
      void commit();
    }
  };

  return (
    <span className={`wd-inline wd-inline-editing ${className ?? ""}`}>
      {multiline ? (
        <Input.TextArea
          ref={inputRef as React.Ref<never>}
          aria-label={ariaLabel}
          value={draft}
          autoSize={{ minRows: 2, maxRows: 12 }}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={onKeyDown}
          onBlur={() => void commit()}
          disabled={saving}
        />
      ) : (
        <Input
          ref={inputRef as React.Ref<never>}
          aria-label={ariaLabel}
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={onKeyDown}
          onBlur={() => void commit()}
          disabled={saving}
        />
      )}
      {saving ? <Spin size="small" /> : null}
      {error ? <span className="wd-inline-error">{error}</span> : null}
    </span>
  );
}

/**
 * InlineSelect edits an enumerated field in place (status, kind). Changing the
 * option saves immediately — there is no separate confirm step.
 */
export function InlineSelect<T extends string>(props: {
  value: T;
  ariaLabel: string;
  disabled?: boolean;
  className?: string;
  options: { value: T; label: ReactNode }[];
  onSave: (next: T) => Promise<unknown> | unknown;
  render?: (value: T) => ReactNode;
}) {
  const { value, ariaLabel, disabled, className, options, onSave, render } = props;
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);

  const commit = async (next: T) => {
    setSaving(true);
    try {
      await onSave(next);
    } finally {
      setSaving(false);
      setEditing(false);
    }
  };

  if (!editing) {
    return (
      <span className={`wd-inline ${className ?? ""}`}>
        <button
          type="button"
          className="wd-inline-value"
          aria-label={ariaLabel}
          disabled={disabled}
          onClick={() => !disabled && setEditing(true)}
        >
          {render ? render(value) : value}
        </button>
      </span>
    );
  }

  return (
    <span className={`wd-inline wd-inline-editing ${className ?? ""}`}>
      <Select
        autoFocus
        size="small"
        aria-label={ariaLabel}
        value={value}
        options={options}
        loading={saving}
        onChange={(next) => void commit(next)}
        onBlur={() => setEditing(false)}
      />
    </span>
  );
}
