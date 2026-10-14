import { helper } from './util';

declare function useCallback<T>(f: T, deps: unknown[]): T;
declare function useMemo<T>(f: () => T, deps: unknown[]): T;

export function Panel(flag: boolean) {
  function inner() {
    helper();
  }
  const onSave = useCallback(() => {
    inner();
  }, []);
  const value = useMemo(() => helper(), []);
  const handle = {
    save() {
      onSave();
    },
    nested: {
      close: () => helper(),
    },
  };
  [1].forEach(() => helper());
  if (flag) {
    const twin = () => helper();
    twin();
  } else {
    const twin = () => inner();
    twin();
  }
  return [value, handle];
}
