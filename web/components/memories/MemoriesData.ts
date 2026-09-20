export interface MemoryComment {
  id: string;
  author: string;
  avatar: string;
  time: string;
  text: string;
}

export interface MemoryItem {
  id: string;
  title: string;
  subtitle: string;
  day: string;
  dayNumber: number;
  time: string;
  location: string;
  image: string;
  alt: string;
  author: {
    name: string;
    avatar: string;
    role?: string;
  };
  note: string;
  story: string;
  category: "all" | "beaches" | "architecture" | "food" | "sunset" | "moments";
  vibe?: string;
  audioLabel?: string;
  audioDuration?: string;
  reactions: {
    heart: number;
    fire: number;
    sparkle: number;
  };
  comments: MemoryComment[];
}


export interface RecommendedPlace {
  id: string;
  name: string;
  area: string;
  category: string;
  verdicts: string[];
  highlight: string;
  bestTime?: string;
}

export const GOA_MEMORIES_DATA: MemoryItem[] = [
  {
    id: "goa-dawn",
    title: "First Light at Palolem",
    subtitle: "A silent bay before the morning tide",
    day: "Day 1",
    dayNumber: 1,
    time: "06:15 AM · Dawn",
    location: "Palolem Beach, South Goa",
    image: "/memories/goa/goa-dawn.jpg",
    alt: "A quiet beach in Goa at first light with calm water reflecting the morning sky",
    author: {
      name: "Alice",
      avatar: "A",
      role: "Organizer",
    },
    note: "We arrived before the beach woke up. The sea was glass, and for forty minutes nobody said a word.",
    story: "After the red-eye flight and a quiet dawn drive through coastal villages, we walked straight down to the shoreline. The air smelled of salt and damp sand. The entire bay was completely silent except for small ripples kissing the shore.",
    category: "beaches",
    audioLabel: "Morning tide & quiet waves",
    audioDuration: "0:08",
    reactions: {
      heart: 14,
      fire: 6,
      sparkle: 9,
    },
    comments: [
      {
        id: "c1",
        author: "Bob",
        avatar: "B",
        time: "1 day ago",
        text: "Waking up at 5:30 was rough, but walking onto this beach made it completely worth it.",
      },
      {
        id: "c2",
        author: "Clara",
        avatar: "C",
        time: "1 day ago",
        text: "The reflection on the water in this shot is serene.",
      },
    ],
  },
  {
    id: "goa-palms",
    title: "The Coconut Canopy Trail",
    subtitle: "Taking the scenic scooter route through Morjim",
    day: "Day 2",
    dayNumber: 2,
    time: "10:45 AM · Morning",
    location: "Morjim & Ashwem Trail, North Goa",
    image: "/memories/goa/goa-palms.jpg",
    alt: "Tall curved palms leaning over a coastal road in Goa",
    author: {
      name: "Bob",
      avatar: "B",
      role: "Navigator",
    },
    note: "The map lost signal near the river, so we just followed the palms until we could hear waves again.",
    story: "We rented four scooters and promised ourselves we'd avoid the highway. Passing through narrow roads lined with hundreds of bent coconut trees, warm dappled sunlight danced on the asphalt. The long way back was always the better way.",
    category: "moments",
    audioLabel: "Coastal breeze through palms",
    audioDuration: "0:06",
    reactions: {
      heart: 11,
      fire: 15,
      sparkle: 4,
    },
    comments: [
      {
        id: "c3",
        author: "Alice",
        avatar: "A",
        time: "1 day ago",
        text: "Best ride of the entire week.",
      },
      {
        id: "c4",
        author: "David",
        avatar: "D",
        time: "18h ago",
        text: "Clean roads and great shade all the way.",
      },
    ],
  },
  {
    id: "goa-market",
    title: "Fontainhas Latin Quarter",
    subtitle: "Ochre stone walls, Portuguese tiles, and quiet afternoons",
    day: "Day 3",
    dayNumber: 3,
    time: "02:30 PM · Midday",
    location: "Fontainhas, Panjim",
    image: "/memories/goa/goa-market.jpg",
    alt: "Warm historic stone architecture and lush palms in the old quarters",
    author: {
      name: "Clara",
      avatar: "C",
      role: "Photographer",
    },
    note: "A slow afternoon between old ochre walls, brass nameplates, and fresh warm Bebinca.",
    story: "Escaping the midday sun, we wandered through the historic Portuguese quarter of Panjim. Narrow cobblestone alleys, bright yellow and terracotta facades with wooden balconies, and the smell of freshly baked pastries drifting from heritage bakeries.",
    category: "architecture",
    audioLabel: "Street footsteps & chapel bells",
    audioDuration: "0:10",
    reactions: {
      heart: 19,
      fire: 8,
      sparkle: 12,
    },
    comments: [
      {
        id: "c5",
        author: "Clara",
        avatar: "C",
        time: "2 days ago",
        text: "The architecture details here are unbelievable.",
      },
    ],
  },
  {
    id: "goa-coast",
    title: "The Vagator Cove Swim",
    subtitle: "Red laterite cliffs and crystal clear waters",
    day: "Day 4",
    dayNumber: 4,
    time: "04:15 PM · Afternoon",
    location: "Little Vagator Beach",
    image: "/memories/goa/goa-coast.jpg",
    alt: "Deep blue water crashing against red cliffs and golden sand in Goa",
    author: {
      name: "David",
      avatar: "D",
      role: "Swimmer",
    },
    note: "No one checked the time after this swim. We stayed in the water until our fingertips wrinkled.",
    story: "We climbed down the rocky steps from the cliff edge down to this secluded cove. The water was refreshing, breaking in gentle turquoise swells against the dark basalt rocks.",
    category: "beaches",
    audioLabel: "Cliffside surf & sea spray",
    audioDuration: "0:09",
    reactions: {
      heart: 22,
      fire: 18,
      sparkle: 14,
    },
    comments: [
      {
        id: "c6",
        author: "Alice",
        avatar: "A",
        time: "2 days ago",
        text: "This was the highlight of the trip for me.",
      },
    ],
  },
  {
    id: "goa-cafe",
    title: "Candlelight at Assagao",
    subtitle: "Heritage courtyard dinner under ancient banyan trees",
    day: "Day 5",
    dayNumber: 5,
    time: "08:45 PM · Night",
    location: "Gunpowder Courtyard, Assagao",
    image: "/memories/goa/goa-cafe.jpg",
    alt: "A warmly lit rustic table in an outdoor garden courtyard ready for a feast",
    author: {
      name: "Alice",
      avatar: "A",
      role: "Foodie",
    },
    note: "One more round became the whole evening plan. We closed the courtyard down.",
    story: "Hidden inside a restored Goan heritage villa garden. Candlelight danced over antique wooden tables, string lights hung from the banyan branches, and we spent three hours talking and laughing.",
    category: "food",
    audioLabel: "Acoustic guitar & courtyard chatter",
    audioDuration: "0:12",
    reactions: {
      heart: 25,
      fire: 14,
      sparkle: 17,
    },
    comments: [
      {
        id: "c7",
        author: "David",
        avatar: "D",
        time: "3 days ago",
        text: "The mutton curry and appams were top tier.",
      },
    ],
  },
  {
    id: "goa-evening",
    title: "The Golden Sunset Ridge",
    subtitle: "The final drive overlooking the Arabian Sea",
    day: "Day 6",
    dayNumber: 6,
    time: "06:40 PM · Sunset",
    location: "Chapora Fort Overlook",
    image: "/memories/goa/goa-evening.jpg",
    alt: "A warm sunset road glowing through coastal hills",
    author: {
      name: "Clara",
      avatar: "C",
      role: "Photographer",
    },
    note: "The last drive felt like trying to slow down time. We pulled over just to watch the horizon turn violet.",
    story: "On our final evening, the sky turned into shades of violet, rose, and deep twilight. We stopped our scooters along the ridge road and stood watching the fishing boats make their way home.",
    category: "sunset",
    audioLabel: "Evening wind & distance waves",
    audioDuration: "0:07",
    reactions: {
      heart: 31,
      fire: 24,
      sparkle: 29,
    },
    comments: [
      {
        id: "c8",
        author: "Bob",
        avatar: "B",
        time: "3 days ago",
        text: "Next year, let's make it two weeks.",
      },
    ],
  },
];

export const GOA_RECOMMENDATIONS: RecommendedPlace[] = [
  {
    id: "rec-1",
    name: "Gunpowder",
    area: "Assagao, North Goa",
    category: "Dining & Drinks",
    verdicts: ["Must-Visit", "4/4 Group Loved", "Heritage Courtyard"],
    highlight: "Order the Kerala Mutton with Appams and Kokum cocktails.",
    bestTime: "Dinner (Book 1 day ahead)",
  },
  {
    id: "rec-2",
    name: "Little Vagator Cove",
    area: "Vagator Headland",
    category: "Coast & Swim",
    verdicts: ["Best Sunset Spot", "Secluded Beach", "Clean Water"],
    highlight: "Climb down the stone steps by 5:00 PM for golden hour.",
    bestTime: "Late Afternoon · 4:30 PM",
  },
  {
    id: "rec-3",
    name: "Fontainhas Old Quarter",
    area: "Panjim",
    category: "Architecture & Walk",
    verdicts: ["Heritage Walk", "Portuguese Villas", "Great Photos"],
    highlight: "Walk through San Thome alleyways and visit local heritage bakeries.",
    bestTime: "Early Morning or Late Afternoon",
  },
  {
    id: "rec-4",
    name: "Palolem South Bay",
    area: "Canacona, South Goa",
    category: "Beach & Calm",
    verdicts: ["Glassy Water", "Quiet Sunrise", "No Crowds"],
    highlight: "Arrive at 6:00 AM before the beach shacks open.",
    bestTime: "Dawn · 6:00 AM",
  },
];

export const TRIP_BUDGET_RECAP = {
  currency: "₹",
  plannedBudget: 45000,
  actualSpent: 41200,
  savingsTotal: 3800,
  perPersonPlanned: 11250,
  perPersonActual: 10300,
  perPersonSaved: 950,
  membersCount: 4,
  breakdown: [
    { category: "Lodging & Villas", planned: 22000, actual: 20500 },
    { category: "Food, Cafes & Drinks", planned: 14000, actual: 13400 },
    { category: "Scooters & Transit", planned: 5500, actual: 4800 },
    { category: "Activities & Visits", planned: 3500, actual: 2500 },
  ],
};

export const TRIP_RECAP_STATS = {
  daysCount: 6,
  framesCount: 6,
  placesExplored: 14,
  favoriteMoment: "The Vagator Cove Swim (Unanimous 4/4 votes)",
  tripQuote: "“No one checked the time after this swim.”",
  tripQuoteAuthor: "Alice, Day 4",
  friends: [
    { name: "Alice", avatar: "A" },
    { name: "Bob", avatar: "B" },
    { name: "Clara", avatar: "C" },
    { name: "David", avatar: "D" },
  ],
};
