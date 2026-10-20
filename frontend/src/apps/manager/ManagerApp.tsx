import { Routes, Route, NavLink, Link } from 'react-router-dom';
import { useEffect, useState } from 'react';
import { api } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

function Shell({ children }: { children: React.ReactNode }) {
  const { me, logout } = useAuth();
  return (
    <div className="app">
      <aside className="sidebar">
        <h1>Rental Manager</h1>
        <NavLink to="/app" end>Dashboard</NavLink>
        <NavLink to="/app/fleet">Fleet</NavLink>
        <NavLink to="/app/branches">Branches</NavLink>
        <NavLink to="/app/calendar">Calendar</NavLink>
        <NavLink to="/app/customers">Customers</NavLink>
        <NavLink to="/app/bookings">Bookings</NavLink>
        <div style={{ marginTop: 20, borderTop: '1px solid var(--border)', paddingTop: 20 }}>
          <div className="muted" style={{ fontSize: 13 }}>{me?.name}</div>
          <div className="muted" style={{ fontSize: 12 }}>{me?.role}</div>
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
  useEffect(() => { api.get<Record<string, number>>('/manager/stats').then(setS).catch(e => setErr(e.message)); }, []);
  return (
    <>
      <div className="topbar"><h2>Operations overview</h2></div>
      {err && <div className="err">{err}</div>}
      {!s ? <p className="muted">Loading...</p> : (
        <div className="kpi">
          <Card lbl="Fleet size" num={s.fleetSize} />
          <Card lbl="Available" num={s.available} />
          <Card lbl="Rented" num={s.rented} />
          <Card lbl="Maintenance" num={s.maintenance} />
          <Card lbl="Branches" num={s.branches} />
          <Card lbl="Customers" num={s.customers} />
        </div>
      )}
      <p className="muted" style={{ marginTop: 24, fontSize: 13 }}>
        Revenue, utilization and booking KPIs populate in Phase 4 once the reservation engine lands.
      </p>
    </>
  );
}
function Card({ lbl, num }: { lbl: string; num: number }) {
  return <div className="card"><div className="lbl">{lbl}</div><div className="num">{num ?? 0}</div></div>;
}

type Vehicle = { id: string; fleetNumber: string | null; licensePlate: string | null; make: string | null; model: string | null; year: number | null; status: string; dailyRateCents: number; depositCents: number };

function Fleet() {
  const [vs, setVs] = useState<Vehicle[]>([]);
  const [err, setErr] = useState(''); const [ok, setOk] = useState('');
  const [form, setForm] = useState({ fleetNumber: '', licensePlate: '', make: '', model: '', year: new Date().getFullYear(), dailyRateCents: 5000, depositCents: 20000 });
  async function reload() { try { setVs(await api.get<Vehicle[]>('/manager/vehicles')); } catch (e: any) { setErr(e.message); } }
  useEffect(() => { reload(); }, []);
  async function submit(e: React.FormEvent) {
    e.preventDefault(); setErr(''); setOk('');
    try {
      await api.post('/manager/vehicles', { ...form, year: Number(form.year), dailyRateCents: Number(form.dailyRateCents), depositCents: Number(form.depositCents) });
      setOk('Vehicle added'); setForm({ ...form, fleetNumber: '', licensePlate: '', make: '', model: '' });
      reload();
    } catch (e: any) { setErr(e.message); }
  }
  return (
    <>
      <div className="topbar"><h2>Fleet</h2></div>
      <div className="row">
        <div className="card">
          <h3>Vehicles</h3>
          <table>
            <thead><tr><th>Fleet #</th><th>Plate</th><th>Vehicle</th><th>Daily</th><th>Status</th></tr></thead>
            <tbody>
              {vs.length === 0 && <tr><td colSpan={5} className="muted">No vehicles yet.</td></tr>}
              {vs.map(v => (
                <tr key={v.id}>
                  <td>{v.fleetNumber || '—'}</td>
                  <td className="muted">{v.licensePlate || '—'}</td>
                  <td>{[v.year, v.make, v.model].filter(Boolean).join(' ')}</td>
                  <td>${(v.dailyRateCents / 100).toFixed(2)}</td>
                  <td><span className={`badge ${v.status === 'available' ? 'ok' : 'warn'}`}>{v.status}</span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="card">
          <h3>Add vehicle</h3>
          <form onSubmit={submit}>
            <label>Fleet number</label>
            <input value={form.fleetNumber} onChange={e => setForm({ ...form, fleetNumber: e.target.value })} />
            <label>License plate</label>
            <input value={form.licensePlate} onChange={e => setForm({ ...form, licensePlate: e.target.value })} />
            <label>Make</label>
            <input value={form.make} onChange={e => setForm({ ...form, make: e.target.value })} required />
            <label>Model</label>
            <input value={form.model} onChange={e => setForm({ ...form, model: e.target.value })} required />
            <label>Year</label>
            <input type="number" value={form.year} onChange={e => setForm({ ...form, year: Number(e.target.value) })} />
            <label>Daily rate (cents)</label>
            <input type="number" value={form.dailyRateCents} onChange={e => setForm({ ...form, dailyRateCents: Number(e.target.value) })} />
            <label>Deposit (cents)</label>
            <input type="number" value={form.depositCents} onChange={e => setForm({ ...form, depositCents: Number(e.target.value) })} />
            {err && <div className="err">{err}</div>}
            {ok && <div className="ok">{ok}</div>}
            <button type="submit" style={{ marginTop: 12 }}>Add vehicle</button>
          </form>
        </div>
      </div>
    </>
  );
}

type Branch = { id: string; name: string; city: string | null; country: string | null; timezone: string; isActive: boolean };
function Branches() {
  const [list, setList] = useState<Branch[]>([]);
  const [err, setErr] = useState(''); const [ok, setOk] = useState('');
  const [form, setForm] = useState({ name: '', city: '', country: '', address: '', timezone: 'UTC' });
  async function reload() { try { setList(await api.get<Branch[]>('/manager/branches')); } catch (e: any) { setErr(e.message); } }
  useEffect(() => { reload(); }, []);
  async function submit(e: React.FormEvent) {
    e.preventDefault(); setErr(''); setOk('');
    try { await api.post('/manager/branches', form); setOk('Branch created'); setForm({ ...form, name: '', city: '', country: '', address: '' }); reload(); }
    catch (e: any) { setErr(e.message); }
  }
  return (
    <>
      <div className="topbar"><h2>Branches</h2></div>
      <div className="row">
        <div className="card">
          <table>
            <thead><tr><th>Name</th><th>City</th><th>Timezone</th><th>Active</th></tr></thead>
            <tbody>
              {list.length === 0 && <tr><td colSpan={4} className="muted">No branches yet.</td></tr>}
              {list.map(b => (
                <tr key={b.id}><td>{b.name}</td><td>{b.city || '—'}</td><td className="muted">{b.timezone}</td><td>{b.isActive ? 'yes' : 'no'}</td></tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="card">
          <h3>New branch</h3>
          <form onSubmit={submit}>
            <label>Name</label><input value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} required />
            <label>Address</label><input value={form.address} onChange={e => setForm({ ...form, address: e.target.value })} />
            <label>City</label><input value={form.city} onChange={e => setForm({ ...form, city: e.target.value })} />
            <label>Country</label><input value={form.country} onChange={e => setForm({ ...form, country: e.target.value })} />
            <label>Timezone</label><input value={form.timezone} onChange={e => setForm({ ...form, timezone: e.target.value })} />
            {err && <div className="err">{err}</div>}
            {ok && <div className="ok">{ok}</div>}
            <button type="submit" style={{ marginTop: 12 }}>Create branch</button>
          </form>
        </div>
      </div>
    </>
  );
}

function Placeholder({ title }: { title: string }) {
  return (
    <>
      <div className="topbar"><h2>{title}</h2></div>
      <div className="card">
        <p className="muted">
          This module ships in a later phase (see the roadmap in the README). The backend scaffolding
          is in place; the UI and endpoints will be added when their phase begins.
        </p>
      </div>
    </>
  );
}

export default function ManagerApp() {
  return (
    <Shell>
      <Routes>
        <Route index element={<Dashboard />} />
        <Route path="fleet" element={<Fleet />} />
        <Route path="branches" element={<Branches />} />
        <Route path="calendar" element={<Placeholder title="Reservation calendar (Phase 4)" />} />
        <Route path="customers" element={<Placeholder title="Customers (Phase 3)" />} />
        <Route path="bookings" element={<Placeholder title="Bookings (Phase 4)" />} />
        <Route path="*" element={<Link to="/app">Back to dashboard</Link>} />
      </Routes>
    </Shell>
  );
}
