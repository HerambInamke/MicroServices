import { Link } from 'react-router-dom';
import { useApi } from '../hooks';

type Order = {
  id: string;
  status: string;
  total: number;
  currency: string;
  createdAt: string;
  items: { productId: number; qty: number; unitPrice: number }[];
};

export function Orders() {
  const { data, error } = useApi<Order[]>('/orders');
  return (
    <>
      <h1>Orders</h1>
      {error && <p className="error">{error}</p>}
      {data && data.length === 0 && <p>No orders yet.</p>}
      {data && data.length > 0 && (
        <table>
          <thead>
            <tr><th>Placed</th><th>Items</th><th>Total</th><th>Status</th></tr>
          </thead>
          <tbody>
            {data.map((o) => (
              <tr key={o.id}>
                <td>{new Date(o.createdAt).toLocaleString()}</td>
                <td>
                  {o.items.map((i) => (
                    <div key={i.productId}>
                      <Link to={`/products/${i.productId}`}>#{i.productId}</Link> × {i.qty} @ {i.unitPrice}
                    </div>
                  ))}
                </td>
                <td>{o.total} {o.currency}</td>
                <td><span className="badge">{o.status}</span></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}
