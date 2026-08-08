"use client";

import Link from "next/link";
import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { useAuth } from "@/context/AuthContext";

export default function Home() {
  const { status } = useAuth();
  const router = useRouter();

  useEffect(() => {
    if (status === "authenticated") router.replace("/trips");
  }, [status, router]);

  if (status === "loading") return null;

  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-4 p-8">
      <h1 className="text-3xl font-semibold">Junto</h1>
      <p className="text-neutral-500">Collaborative trip planning.</p>
      <div className="flex gap-3">
        <Link href="/login" className="rounded-md bg-blue-600 px-4 py-2 text-white">
          Log in
        </Link>
        <Link href="/signup" className="rounded-md border border-neutral-300 px-4 py-2">
          Sign up
        </Link>
      </div>
    </main>
  );
}
