import { useState, type FormEvent } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../hooks';

type Event = { index: number; type: string; role: string; actorAddress?: string; txHash?: string; timestamp: string };
type Resolved = {
  pieceId: string;
  history: { piece: { description?: string; currentOwnerRole: string }; events: Event[] };
};

// Public: anyone holding a tag can check the piece's chain of custody without logging in.
export function Verify() {
  const { code } = useParams();
  const navigate = useNavigate();
  const [text, setText] = useState(code ?? '');
  const { data, error } = useApi<Resolved>(code ? `/qr/${encodeURIComponent(code)}/resolve` : null);

  function submit(e: FormEvent) {
    e.preventDefault();
    if (text.trim()) navigate(`/verify/${encodeURIComponent(text.trim())}`);
  }

  return (
    <>
      <h1>Verify a piece</h1>
      <form className="row" onSubmit={submit}>
        <input placeholder="Code printed on the tag" value={text} onChange={(e) => setText(e.target.value)} />
        <button type="submit">Verify</button>
      </form>
      {code && error && <p className="error">{error}</p>}
      {code && data && (
        <>
          <p>
            Piece <code>{data.pieceId}</code>
            {data.history.piece.description && ` — ${data.history.piece.description}`}. Now held by a{' '}
            <b>{data.history.piece.currentOwnerRole}</b>.
          </p>
          <ol className="timeline">
            {data.history.events.map((ev) => (
              <li key={ev.index}>
                <b>{ev.role}</b> <span className="muted">{ev.type} · {new Date(ev.timestamp).toLocaleString()}</span>
                {ev.actorAddress && <div className="mono">signed by {ev.actorAddress}</div>}
                {ev.txHash && <div className="mono">tx {ev.txHash}</div>}
              </li>
            ))}
          </ol>
        </>
      )}
    </>
  );
}
