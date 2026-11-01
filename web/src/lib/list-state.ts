"use client";

import * as React from "react";

// Keep list controls when client-side navigation unmounts a page. A full reload clears them.
const values = new Map<string, unknown>();

export function useListState<T>(key: string, initial: T): [T, React.Dispatch<React.SetStateAction<T>>] {
  const [value, setValue] = React.useState<T>(() => (values.has(key) ? (values.get(key) as T) : initial));
  const current = React.useRef(value);

  const update = React.useCallback<React.Dispatch<React.SetStateAction<T>>>((next) => {
    const resolved = typeof next === "function" ? (next as (previous: T) => T)(current.current) : next;
    current.current = resolved;
    values.set(key, resolved);
    setValue(resolved);
  }, [key]);

  return [value, update];
}
