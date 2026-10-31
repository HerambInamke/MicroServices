import { StrictMode, type ReactNode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter, Link, NavLink, Route, Routes, useNavigate } from 'react-router-dom';
import { logout } from './api';
import { useSession } from './hooks';
import { Login } from './pages/Login';
import { Catalog } from './pages/Catalog';
import { Product } from './pages/Product';
import { Orders } from './pages/Orders';
import { Verify } from './pages/Verify';
import { Chat } from './pages/Chat';
import { Notifications } from './pages/Notifications';
import './style.css';

function Layout({ children }: { children: ReactNode }) {
  const session = useSession();
  const navigate = useNavigate();
  return (
    <>
      <header>
        <Link to="/" className="brand">Provenance</Link>
        <nav>
          <NavLink to="/" end>Catalog</NavLink>
          <NavLink to="/verify">Verify</NavLink>
          {session && (
            <>
              <NavLink to="/orders">Orders</NavLink>
              <NavLink to="/chat">Chat</NavLink>
              <NavLink to="/notifications">Notifications</NavLink>
            </>
          )}
        </nav>
        <span className="who">
          {session ? (
            <>
              {session.user.email} <span className="role">{session.user.role}</span>{' '}
              <button onClick={() => logout().then(() => navigate('/'))}>Log out</button>
            </>
          ) : (
            <Link to="/login">Log in</Link>
          )}
        </span>
      </header>
      <main>{children}</main>
    </>
  );
}

function RequireLogin({ children }: { children: ReactNode }) {
  return useSession() ? children : <p>Please <Link to="/login">log in</Link> to see this page.</p>;
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <Layout>
        <Routes>
          <Route path="/" element={<Catalog />} />
          <Route path="/login" element={<Login />} />
          <Route path="/products/:id" element={<Product />} />
          <Route path="/verify/:code?" element={<Verify />} />
          <Route path="/orders" element={<RequireLogin><Orders /></RequireLogin>} />
          <Route path="/chat/:threadId?" element={<RequireLogin><Chat /></RequireLogin>} />
          <Route path="/notifications" element={<RequireLogin><Notifications /></RequireLogin>} />
        </Routes>
      </Layout>
    </BrowserRouter>
  </StrictMode>,
);
