import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { api, setToken } from '../api/client';
import { useAuth } from '../auth/AuthContext';

export default function BootstrapOwner() {
  const nav = useNavigate();
  const { refresh } = useAuth();
  const [name, setName] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr('');
    setBusy(true);
    try {
      const r = await api.post<{ token: string }>('/auth/bootstrap-owner', { name, email, password });
      setToken(r.token);
      await refresh();
      nav('/owner');
    } catch (e: any) {
      setErr(e.message || 'bootstrap failed');
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="center">
      <form className="card auth-card" onSubmit={submit}>
        <h2>Create platform owner</h2>
        <p className="muted" style={{ marginTop: 0 }}>
          This endpoint works only until the first platform owner exists.
        </p>
        <label>Name</label>
        <input value={name} onChange={e => setName(e.target.value)} required autoFocus />
        <label>Email</label>
        <input value={email} onChange={e => setEmail(e.target.value)} type="email" required />
        <label>Password <span className="muted">(min 8 chars)</span></label>
        <input value={password} onChange={e => setPassword(e.target.value)} type="password" minLength={8} required />
        {err && <div className="err">{err}</div>}
        <div style={{ display: 'flex', gap: 12, marginTop: 16 }}>
          <button disabled={busy} type="submit">{busy ? 'Creating...' : 'Create & sign in'}</button>
          <Link to="/login" className="btn secondary" style={{ padding: '10px 16px' }}>Back to sign in</Link>
        </div>
      </form>
    </div>
  );
}
