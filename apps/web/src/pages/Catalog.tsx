import { useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { useApi } from '../hooks';

// Both /products and /search?type=product return these fields (search hits have no moq).
type Item = { id: string | number; name: string; description?: string; basePrice?: number; moq?: number };

export function Catalog() {
  const [text, setText] = useState('');
  const [query, setQuery] = useState('');
  const { data, error } = useApi<Item[]>(query ? `/search?q=${encodeURIComponent(query)}&type=product` : '/products');

  function submit(e: FormEvent) {
    e.preventDefault();
    setQuery(text.trim());
  }

  return (
    <>
      <h1>Catalog</h1>
      <form className="row" onSubmit={submit}>
        <input placeholder="Search sarees" value={text} onChange={(e) => setText(e.target.value)} />
        <button type="submit">Search</button>
      </form>
      {error && <p className="error">{error}</p>}
      {data && data.length === 0 && <p>No products found.</p>}
      <ul className="list">
        {data?.map((p) => (
          <li key={p.id}>
            <Link to={`/products/${p.id}`}>{p.name}</Link>
            <span className="muted">
              {p.basePrice != null && ` ₹${p.basePrice}`}
              {p.moq != null && ` · MOQ ${p.moq}`}
            </span>
          </li>
        ))}
      </ul>
    </>
  );
}
