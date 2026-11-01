"use client";

import * as React from "react";

import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { useListState } from "@/lib/list-state";

export type SortDirection = "asc" | "desc";

export interface SortPreference<Field extends string> {
  field: Field;
  direction: SortDirection;
}

export function useStoredSortPreference<Field extends string>(
  key: string,
  fields: readonly Field[],
  defaultField: Field,
  defaultDirection: SortDirection,
) {
  const [preference, setPreference] = useListState<SortPreference<Field>>(`sort:${key}`, {
    field: defaultField,
    direction: defaultDirection,
  });
  const [hydrated, setHydrated] = React.useState(false);

  React.useEffect(() => {
    const raw = getLocalStorageValue(key);
    if (raw) {
      try {
        const parsed = JSON.parse(raw) as Partial<SortPreference<string>>;
        const field = fields.find((candidate) => candidate === parsed.field);
        const direction = parsed.direction === "asc" || parsed.direction === "desc" ? parsed.direction : null;
        if (field && direction) setPreference((current) =>
          current.field === field && current.direction === direction ? current : { field, direction },
        );
      } catch {
        // Ignore malformed or legacy preferences and retain the current default.
      }
    }
    setHydrated(true);
  }, [fields, key, setPreference]);

  React.useEffect(() => {
    if (!hydrated) return;
    setLocalStorageValue(key, JSON.stringify(preference));
  }, [hydrated, key, preference]);

  return [preference, setPreference] as const;
}
