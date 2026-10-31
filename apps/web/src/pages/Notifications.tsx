import { useState } from 'react';
import { api } from '../api';
import { useApi } from '../hooks';

type Notification = { id: string; title: string; body: string; read: boolean; createdAt: string };

export function Notifications() {
  const { data, error, reload } = useApi<Notification[]>('/notifications');
  const [markError, setMarkError] = useState<string>();

  async function markRead(id: string) {
    setMarkError(undefined);
    try {
      await api('PATCH', `/notifications/${id}/read`);
      reload();
    } catch (err) {
      setMarkError((err as Error).message);
    }
  }

  return (
    <>
      <h1>Notifications</h1>
      {(error || markError) && <p className="error">{error || markError}</p>}
      {data && data.length === 0 && <p>Nothing yet.</p>}
      <ul className="list">
        {data?.map((n) => (
          <li key={n.id} className={n.read ? 'muted' : undefined}>
            <div>
              {n.read ? n.title : <b>{n.title}</b>}
              <div>{n.body}</div>
              <div className="muted">{new Date(n.createdAt).toLocaleString()}</div>
            </div>
            {!n.read && <button onClick={() => markRead(n.id)}>Mark read</button>}
          </li>
        ))}
      </ul>
    </>
  );
}
