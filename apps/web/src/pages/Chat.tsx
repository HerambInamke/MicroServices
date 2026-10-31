import { useEffect, useState, type FormEvent } from 'react';
import { NavLink, useParams } from 'react-router-dom';
import { api, socket } from '../api';
import { useApi, useSession } from '../hooks';

type Thread = { id: string; wholesalerId: string; retailerId: string; lastMessageAt: string };
type Message = {
  id: string;
  senderId: string;
  kind: 'text' | 'offer';
  text?: string;
  offer?: { productId: string; qty: number; unitPrice: number; status: string };
  createdAt: string;
};

export function Chat() {
  const { threadId } = useParams();
  const me = useSession()!.user;
  const threads = useApi<Thread[]>('/chat/threads');
  const messages = useApi<Message[]>(threadId ? `/chat/threads/${threadId}/messages` : null);
  const [text, setText] = useState('');
  const [error, setError] = useState<string>();
  const reload = messages.reload;

  // The socket only receives; any frame means the thread changed, so re-read it.
  useEffect(() => {
    if (!threadId) return;
    const ws = socket(`/chat/${threadId}`);
    ws.onmessage = reload;
    return () => ws.close();
  }, [threadId, reload]);

  async function send(e: FormEvent) {
    e.preventDefault();
    setError(undefined);
    try {
      await api('POST', `/chat/threads/${threadId}/messages`, { text });
      setText('');
      reload();
    } catch (err) {
      setError((err as Error).message);
    }
  }

  return (
    <div className="chat">
      <aside>
        <h2>Conversations</h2>
        {threads.error && <p className="error">{threads.error}</p>}
        {threads.data?.length === 0 && <p className="muted">None yet. Start one from a product page.</p>}
        {threads.data?.map((t) => (
          <NavLink key={t.id} to={`/chat/${t.id}`}>
            with {me.id === t.wholesalerId ? `retailer ${t.retailerId}` : `wholesaler ${t.wholesalerId}`}
          </NavLink>
        ))}
      </aside>
      <section>
        {!threadId && <p className="muted">Pick a conversation.</p>}
        {messages.error && <p className="error">{messages.error}</p>}
        {/* the API returns newest first */}
        {messages.data && [...messages.data].reverse().map((m) => (
          <div key={m.id} className={m.senderId === me.id ? 'msg mine' : 'msg'}>
            {m.kind === 'text'
              ? m.text
              : `Offer: ${m.offer!.qty} × product ${m.offer!.productId} at ₹${m.offer!.unitPrice} (${m.offer!.status})`}
            <div className="muted">{new Date(m.createdAt).toLocaleString()}</div>
          </div>
        ))}
        {threadId && (
          <form className="row" onSubmit={send}>
            <input required maxLength={2000} placeholder="Message" value={text} onChange={(e) => setText(e.target.value)} />
            <button type="submit">Send</button>
          </form>
        )}
        {error && <p className="error">{error}</p>}
      </section>
    </div>
  );
}
