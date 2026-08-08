"use client";

import { createContext, useCallback, useContext, useEffect, useState } from "react";
import { usePathname } from "next/navigation";
import * as authApi from "@/lib/api/auth";
import { refreshAccessToken } from "@/lib/http";
import { getAccessToken, subscribeToken } from "@/lib/token-store";
import { resetTripSocket } from "@/lib/socket";
import type { User } from "@/lib/types";

interface AuthState {
  user: User | null;
  status: "loading" | "authenticated" | "anonymous";
  login: (email: string, password: string) => Promise<void>;
  signup: (email: string, password: string, displayName: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const pathname = usePathname();
  // /login and /signup don't need a restored session to be usable, and attempting one costs a
  // call against the auth endpoints' strict shared rate limit (D35/D36) for zero benefit on a
  // page whose whole point is that the visitor isn't authenticated yet. Captured once at first
  // render — a later client-side navigation away from /login must not retroactively skip this.
  const [status, setStatus] = useState<AuthState["status"]>(() =>
    pathname === "/login" || pathname === "/signup" ? "anonymous" : "loading"
  );
  const [skipRestore] = useState(() => pathname === "/login" || pathname === "/signup");

  // On first load there is no access token in memory (D30) — try the refresh cookie before
  // giving up and treating the visitor as anonymous.
  useEffect(() => {
    if (skipRestore) return;
    let cancelled = false;
    (async () => {
      const ok = await refreshAccessToken();
      if (!ok) {
        if (!cancelled) setStatus("anonymous");
        return;
      }
      try {
        const me = await authApi.me();
        if (!cancelled) {
          setUser(me);
          setStatus("authenticated");
        }
      } catch {
        if (!cancelled) setStatus("anonymous");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [skipRestore]);

  useEffect(() => {
    return subscribeToken(() => {
      if (getAccessToken() === null) {
        setUser(null);
        setStatus("anonymous");
      }
    });
  }, []);

  const login = useCallback(async (email: string, password: string) => {
    const session = await authApi.login(email, password);
    setUser(session.user);
    setStatus("authenticated");
  }, []);

  const signup = useCallback(async (email: string, password: string, displayName: string) => {
    await authApi.signup(email, password, displayName);
  }, []);

  const logout = useCallback(async () => {
    await authApi.logout();
    resetTripSocket();
    setUser(null);
    setStatus("anonymous");
  }, []);

  return (
    <AuthContext.Provider value={{ user, status, login, signup, logout }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
