import { useSyncExternalStore } from "react";

// Reading a browser-only value (the user's IANA zone, the platform's zone list)
// during render is a hydration hazard: the prerender happens on a build machine
// whose zone is not the reader's. useSyncExternalStore is the API built for
// exactly this — it takes a separate server snapshot, so the markup React
// produces on the server and the markup it hydrates with agree by construction,
// and the real value arrives on the client re-render.
//
// Snapshots are module-cached because useSyncExternalStore compares by
// reference: returning a freshly-built array each call would loop forever.

const subscribe = () => () => {};

let cachedZone: string | null = null;
function clientZone(): string {
  if (cachedZone === null) {
    cachedZone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  }
  return cachedZone;
}
const serverZone = () => "UTC";

const SERVER_ZONE_LIST = ["UTC"];
let cachedZoneList: string[] | null = null;
function clientZoneList(): string[] {
  if (cachedZoneList === null) {
    const supported = (
      Intl as typeof Intl & { supportedValuesOf?: (key: string) => string[] }
    ).supportedValuesOf;
    cachedZoneList = supported ? supported("timeZone") : SERVER_ZONE_LIST;
  }
  return cachedZoneList;
}
const serverZoneList = () => SERVER_ZONE_LIST;

/** The reader's own IANA zone — a sensible default for a new trip, because D7
 *  makes the trip's zone load-bearing for every slot time on the itinerary. */
export function useBrowserTimeZone(): string {
  return useSyncExternalStore(subscribe, clientZone, serverZone);
}

export function useTimeZoneList(): string[] {
  return useSyncExternalStore(subscribe, clientZoneList, serverZoneList);
}
