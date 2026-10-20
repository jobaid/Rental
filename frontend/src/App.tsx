import { Routes, Route, Navigate } from 'react-router-dom';
import { useAuth } from './auth/AuthContext';
import Landing from './pages/Landing';
import Login from './pages/Login';
import BootstrapOwner from './pages/BootstrapOwner';
import OwnerApp from './apps/owner/OwnerApp';
import ManagerApp from './apps/manager/ManagerApp';
import CustomerApp from './apps/customer/CustomerApp';

function ComingSoon() {
  return (
    <section style={{ maxWidth: 720, margin: '48px auto', padding: 24 }}>
      <div className="card">
        <h2>Coming soon</h2>
        <p className="muted">
          This part of the public site arrives in Phase 5 (customer booking engine &amp; portal).
        </p>
      </div>
    </section>
  );
}

function RequireRole({ role, children }: { role: 'platform_owner' | 'tenant'; children: JSX.Element }) {
  const { me, loading } = useAuth();
  if (loading) return <div className="center">Loading...</div>;
  if (!me) return <Navigate to="/login" replace />;
  if (role === 'platform_owner' && me.role !== 'platform_owner') return <Navigate to="/" replace />;
  if (role === 'tenant' && me.role === 'platform_owner') return <Navigate to="/owner" replace />;
  if (role === 'tenant' && !me.companyId) return <Navigate to="/" replace />;
  return children;
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/setup" element={<BootstrapOwner />} />

      <Route path="/owner/*" element={<RequireRole role="platform_owner"><OwnerApp /></RequireRole>} />
      <Route path="/app/*" element={<RequireRole role="tenant"><ManagerApp /></RequireRole>} />

      <Route path="/*" element={<CustomerApp />}>
        <Route index element={<Landing />} />
        <Route path="*" element={<ComingSoon />} />
      </Route>
    </Routes>
  );
}
