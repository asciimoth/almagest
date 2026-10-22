export function tabHash(name: string): string {
  return `#${encodeURIComponent(name)}`;
}

export function tabNameFromHash(hash: string, names: readonly string[], fallback: string): string {
  if (hash === "#examples" && names.includes("snapshots")) return "snapshots";
  const name = names.find((candidate) => tabHash(candidate) === hash);
  return name ?? fallback;
}
