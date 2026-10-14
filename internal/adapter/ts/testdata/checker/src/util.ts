export function helper(n: number): number {
  return n + 1;
}

export const twice = (n: number) => helper(helper(n));

export class Store {
  private items: string[] = [];
  add(s: string) {
    this.items.push(s);
    helper(1);
  }
  get size() {
    return this.items.length;
  }
}

export interface Service {
  open(path: string): void;
  name: string;
}

export type Handlers = {
  onSave(): void;
  label: string;
};

export const api = {
  load() {
    return helper(2);
  },
  save: () => twice(3),
  version: 1,
};

export default function () {
  return new Store();
}

export enum Mode {
  A,
  B,
}

export interface Closer {
  close(): void;
}

export interface Saver extends Closer {
  save(): void;
}

export class BaseImpl {
  close() {}
}

export class DiskStore extends BaseImpl implements Saver {
  save() {
    helper(4);
  }
}

export class FileService implements Service {
  name = 'f';
  open(path: string) {
    helper(path.length);
  }
}

export class Oops extends Error {}
