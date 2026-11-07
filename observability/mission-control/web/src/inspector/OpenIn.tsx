import type { Link } from '../api/types';

function safe(url: string): boolean {
  try {
    const p = new URL(url).protocol;
    return p === 'http:' || p === 'https:';
  } catch {
    return false;
  }
}

/** "Open in" links to the observability tools: only the configured ones, http(s) only, labels as text. Nothing renders without links. */
export function OpenIn({ links }: { links: Link[] }) {
  const ok = links.filter((l) => safe(l.url));
  if (ok.length === 0) return null;
  return (
    <div className="mc-openin">
      <span className="mc-muted">Open in </span>
      {ok.map((l) => (
        <a key={l.url} href={l.url} target="_blank" rel="noopener noreferrer">
          {l.label}
        </a>
      ))}
    </div>
  );
}
