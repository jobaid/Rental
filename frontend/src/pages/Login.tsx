import { useState } from 'react';
import { useNavigate, Link } from 'react-router-dom';
import { useAuth } from '../auth/AuthContext';

export default function Login() {
  const nav = useNavigate();
  const { login } = useAuth();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [companySlug, setCompanySlug] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr('');
    setBusy(true);
    try {
      const me = await login(email, password, companySlug || undefined);
      if (me.role === 'platform_owner') nav('/owner');
      else if (me.companyId) nav('/app');
      else nav('/');
    } catch (e: any) {
      setErr(e.message || 'login failed');
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="center">
      <form className="card auth-card" onSubmit={submit}>
        <h2>Sign in</h2>
        <p className="muted" style={{ marginTop: 0 }}>
          Platform owners leave company empty. Tenant staff supply their company slug.
        </p>
        <label>Email</label>
        <input value={email} onChange={e => setEmail(e.target.value)} type="email" required autoFocus />
        <label>Password</label>
        <input value={password} onChange={e => setPassword(e.target.value)} type="password" required />
        <label>Company slug <span className="muted">(tenant users only)</span></label>
        <input value={companySlug} onChange={e => setCompanySlug(e.target.value)} placeholder="e.g. acme-rentals" />
        {err && <div className="err">{err}</div>}
        <div style={{ display: 'flex', gap: 12, marginTop: 16 }}>
          <button disabled={busy} type="submit">{busy ? 'Signing in...' : 'Sign in'}</button>
          <Link to="/" className="btn secondary" style={{ padding: '10px 16px' }}>Cancel</Link>
        </div>
        <p className="muted" style={{ marginTop: 20, fontSize: 13 }}>
          First-time setup? <Link to="/setup">Create the platform owner</Link>.
        </p>
      </form>
    </div>
  );
}
