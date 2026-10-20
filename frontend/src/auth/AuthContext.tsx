import { createContext, useContext, useEffect, useState, ReactNode } from 'react';
import { api, getToken, setToken, Me } from '../api/client';

type AuthState = {
  me: Me | null;
  loading: boolean;
  login: (email: string, password: string, companySlug?: string) => Promise<Me>;
  logout: () => void;
  refresh: () => Promise<void>;
};

const Ctx = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null>(null);
  const [loading, setLoading] = useState<boolean>(true);

  async function refresh() {
    if (!getToken()) { setMe(null); setLoading(false); return; }
    try {
      const m = await api.get<Me>('/me');
      setMe(m);
    } catch {
      setToken(null);
      setMe(null);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => { refresh(); }, []);

  async function login(email: string, password: string, companySlug?: string) {
    const r = await api.post<{ token: string; userId: string; role: string; companyId: string | null; name: string }>(
      '/auth/login', { email, password, companySlug }
    );
    setToken(r.token);
    const m: Me = { userId: r.userId, role: r.role, companyId: r.companyId, name: r.name };
    setMe(m);
    return m;
  }

  function logout() { setToken(null); setMe(null); }

  return <Ctx.Provider value={{ me, loading, login, logout, refresh }}>{children}</Ctx.Provider>;
}

export function useAuth() {
  const v = useContext(Ctx);
  if (!v) throw new Error('useAuth outside provider');
  return v;
}
