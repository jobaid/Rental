import { Outlet, Link, useLocation } from 'react-router-dom';

export default function CustomerApp() {
  const loc = useLocation();
  return (
    <>
      <header style={{ padding: '16px 32px', display: 'flex', justifyContent: 'space-between', alignItems: 'center', borderBottom: '1px solid var(--border)' }}>
        <Link to="/" style={{ color: 'var(--text)', fontWeight: 700, fontSize: 18 }}>rentalsoftware</Link>
        <nav style={{ display: 'flex', gap: 16 }}>
          <Link to="/">Home</Link>
          <Link to="/search">Vehicles</Link>
          <Link to="/login">Sign in</Link>
        </nav>
      </header>
      <Outlet />
      <footer style={{ padding: 32, textAlign: 'center', color: 'var(--muted)', fontSize: 13, borderTop: '1px solid var(--border)', marginTop: 48 }}>
        <div>rentalsoftware · multi-tenant fleet &amp; rental platform</div>
        <div style={{ marginTop: 6 }}>{loc.pathname}</div>
      </footer>
    </>
  );
}
