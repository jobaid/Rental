import { Routes, Route, NavLink, Link } from 'react-router-dom';
import { useEffect, useState } from 'react';
import { api } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

function Shell({ children }: { children: React.ReactNode }) {
  const { me, logout } = useAuth();
  return (
    <div className="app">
      <aside className="sidebar">
        <h1>Owner Admin</h1>
        <NavLink to="/owner" end>Dashboard</NavLink>
        <NavLink to="/owner/companies">Companies</NavLink>
        <NavLink to="/owner/plans">Plans</NavLink>
        <div style={{ marginTop: 20, borderTop: '1px solid var(--border)', paddingTop: 20 }}>
          <div className="muted" style={{ fontSize: 13 }}>{me?.name}</div>
          <button className="secondary" onClick={logout} style={{ marginTop: 8, width: '100%' }}>Sign out</button>
        </div>
      </aside>
      <main className="main">{children}</main>
    </div>
  );
}

function Dashboard() {
  const [s, setS] = useState<Record<string, number> | null>(null);
  const [err, setErr] = useState('');
  useEffect(() => { api.get<Record<string, number>>('/owner/stats').then(setS).catch(e => setErr(e.message)); }, []);
  return (
    <>
      <div className="topbar"><h2>Platform overview</h2></div>
      {err && <div className="err">{err}</div>}
      {!s ? <p className="muted">Loading...</p> : (
        <div className="kpi">
          <Card lbl="Companies" num={s.companies} />
          <Card lbl="Active" num={s.companiesActive} />
          <Card lbl="Suspended" num={s.companiesSuspended} />
          <Card lbl="Vehicles" num={s.vehicles} />
          <Card lbl="Tenant users" num={s.users} />
        </div>
      )}
    </>
  );
}
function Card({ lbl, num }: { lbl: string; num: number }) {
  return <div className="card"><div className="lbl">{lbl}</div><div className="num">{num ?? 0}</div></div>;
}

type Plan = { id: string; code: string; name: string; monthlyPriceCents: number; annualPriceCents: number };
type Company = { id: string; name: string; slug: string; status: string; planCode?: string; billingCycle?: string; createdAt: string };

function Companies() {
  const [list, setList] = useState<Company[]>([]);
  const [plans, setPlans] = useState<Plan[]>([]);
  const [err, setErr] = useState('');
  const [ok, setOk] = useState('');
  const [form, setForm] = useState({ name: '', slug: '', planCode: 'starter', billingCycle: 'monthly', ownerName: '', ownerEmail: '', ownerPassword: '' });

  async function reload() {
    try { setList(await api.get<Company[]>('/owner/companies')); } catch (e: any) { setErr(e.message); }
  }
  useEffect(() => {
    reload();
    api.get<Plan[]>('/plans').then(setPlans).catch(() => {});
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr(''); setOk('');
    try {
      await api.post('/owner/companies', form);
      setOk(`Created ${form.slug}`);
      setForm({ ...form, name: '', slug: '', ownerName: '', ownerEmail: '', ownerPassword: '' });
      reload();
    } catch (e: any) { setErr(e.message); }
  }

  return (
    <>
      <div className="topbar"><h2>Companies</h2></div>
      <div className="row">
        <div className="card">
          <h3>Tenants</h3>
          <table>
            <thead><tr><th>Name</th><th>Slug</th><th>Plan</th><th>Status</th></tr></thead>
            <tbody>
              {list.length === 0 && <tr><td colSpan={4} className="muted">No companies yet.</td></tr>}
              {list.map(c => (
                <tr key={c.id}>
                  <td>{c.name}</td>
                  <td className="muted">{c.slug}</td>
                  <td>{c.planCode || '—'}</td>
                  <td><span className={`badge ${c.status === 'active' ? 'ok' : 'warn'}`}>{c.status}</span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="card">
          <h3>New company</h3>
          <form onSubmit={submit}>
            <label>Company name</label>
            <input value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} required />
            <label>Slug <span className="muted">(lowercase, used for login)</span></label>
            <input value={form.slug} onChange={e => setForm({ ...form, slug: e.target.value })} required pattern="[a-z0-9][a-z0-9-]{1,38}[a-z0-9]" />
            <label>Plan</label>
            <select value={form.planCode} onChange={e => setForm({ ...form, planCode: e.target.value })}>
              {plans.map(p => <option key={p.code} value={p.code}>{p.name}</option>)}
            </select>
            <label>Billing cycle</label>
            <select value={form.billingCycle} onChange={e => setForm({ ...form, billingCycle: e.target.value })}>
              <option value="monthly">Monthly</option>
              <option value="annual">Annual</option>
            </select>
            <label>First company-owner name</label>
            <input value={form.ownerName} onChange={e => setForm({ ...form, ownerName: e.target.value })} required />
            <label>Owner email</label>
            <input type="email" value={form.ownerEmail} onChange={e => setForm({ ...form, ownerEmail: e.target.value })} required />
            <label>Owner initial password</label>
            <input type="password" minLength={8} value={form.ownerPassword} onChange={e => setForm({ ...form, ownerPassword: e.target.value })} required />
            {err && <div className="err">{err}</div>}
            {ok && <div className="ok">{ok}</div>}
            <button type="submit" style={{ marginTop: 12 }}>Create company</button>
          </form>
        </div>
      </div>
    </>
  );
}

function Plans() {
  const [plans, setPlans] = useState<Plan[]>([]);
  useEffect(() => { api.get<Plan[]>('/plans').then(setPlans); }, []);
  return (
    <>
      <div className="topbar"><h2>Subscription plans</h2></div>
      <div className="card">
        <table>
          <thead><tr><th>Code</th><th>Name</th><th>Monthly</th><th>Annual</th></tr></thead>
          <tbody>
            {plans.map(p => (
              <tr key={p.id}>
                <td className="muted">{p.code}</td>
                <td>{p.name}</td>
                <td>${(p.monthlyPriceCents / 100).toFixed(2)}</td>
                <td>${(p.annualPriceCents / 100).toFixed(2)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className="muted" style={{ marginTop: 12, fontSize: 13 }}>
          Plan editing UI is not yet implemented in Phase 1. Plans are seeded by the initial migration.
        </p>
      </div>
    </>
  );
}

export default function OwnerApp() {
  return (
    <Shell>
      <Routes>
        <Route index element={<Dashboard />} />
        <Route path="companies" element={<Companies />} />
        <Route path="plans" element={<Plans />} />
        <Route path="*" element={<Link to="/owner">Back to dashboard</Link>} />
      </Routes>
    </Shell>
  );
}
