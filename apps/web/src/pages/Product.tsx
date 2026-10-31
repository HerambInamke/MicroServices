import { useState, type FormEvent } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { api } from '../api';
import { useApi, useSession } from '../hooks';

type Product = {
  id: number;
  name: string;
  description?: string;
  wholesalerId: string;
  moq: number;
  basePrice: number;
  priceTiers: { minQty: number; unitPrice: number }[];
};

export function Product() {
  const { id } = useParams();
  const session = useSession();
  const navigate = useNavigate();
  const { data: p, error } = useApi<Product>(`/products/${id}`);
  const [qty, setQty] = useState('');
  const [actionError, setActionError] = useState<string>();

  async function run(action: () => Promise<void>) {
    setActionError(undefined);
    try {
      await action();
    } catch (err) {
      setActionError((err as Error).message);
    }
  }

  function order(e: FormEvent) {
    e.preventDefault();
    run(async () => {
      await api('POST', '/orders', { items: [{ productId: p!.id, qty: Number(qty) }] });
      navigate('/orders');
    });
  }

  function message() {
    run(async () => {
      const thread = await api<{ id: string }>('POST', '/chat/threads', { participantId: p!.wholesalerId });
      navigate(`/chat/${thread.id}`);
    });
  }

  if (error) return <p className="error">{error}</p>;
  if (!p) return <p>Loading…</p>;
  return (
    <>
      <h1>{p.name}</h1>
      {p.description && <p>{p.description}</p>}
      <p>
        Minimum order: <b>{p.moq}</b> · Base price: <b>₹{p.basePrice}</b>
      </p>
      {p.priceTiers.length > 0 && (
        <table>
          <thead>
            <tr><th>From qty</th><th>Unit price</th></tr>
          </thead>
          <tbody>
            {p.priceTiers.map((t) => (
              <tr key={t.minQty}><td>{t.minQty}</td><td>₹{t.unitPrice}</td></tr>
            ))}
          </tbody>
        </table>
      )}
      {session?.user.role === 'retailer' && (
        <>
          <form className="row" onSubmit={order}>
            <input type="number" min={p.moq} required placeholder={`Qty (min ${p.moq})`} value={qty} onChange={(e) => setQty(e.target.value)} />
            <button type="submit">Place order</button>
          </form>
          <button onClick={message}>Message the wholesaler</button>
        </>
      )}
      {actionError && <p className="error">{actionError}</p>}
    </>
  );
}
