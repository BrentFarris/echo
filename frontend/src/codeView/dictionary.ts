/**
 * Dictionary utilities for spell checking in the code editor.
 *
 * Provides word validation, suggestion generation, and identifier splitting
 * so that camelCase / PascalCase / snake_case identifiers are tokenized into
 * individual words before dictionary lookup.
 */

// ─── Identifier splitting ──────────────────────────────────────

/**
 * Split a programming identifier (camelCase, PascalCase, snake_case, etc.)
 * into an array of lowercase words for spell-checking.
 *
 * Examples:
 *   "codeMirror"    → ["code", "mirror"]
 *   "XMLParser"     → ["xml", "parser"]
 *   "snake_case"    → ["snake", "case"]
 *   "HTTPResponse"  → ["http", "response"]
 */
export function splitIdentifier(identifier: string): string[] {
  if (!identifier) return [];

  // Remove leading/trailing non-word characters
  const trimmed = identifier.replace(/^[^a-zA-Z0-9]+|[^a-zA-Z0-9]+$/g, "");
  if (!trimmed) return [];

  const words: string[] = [];
  let current = "";

  for (let i = 0; i < trimmed.length; i++) {
    const ch = trimmed[i];
    const next = i + 1 < trimmed.length ? trimmed[i + 1] : "";
    const prev = i > 0 ? trimmed[i - 1] : "";

    if (ch === "_") {
      // Underscore separator — flush current word and continue
      if (current) {
        words.push(current.toLowerCase());
        current = "";
      }
    } else if (/[a-zA-Z]/.test(ch)) {
      if (ch === ch.toUpperCase()) {
        // Uppercase letter
        if (current) {
          // Check if we're starting an acronym run or a new word
          const lastChar = current[current.length - 1];
          if (lastChar === lastChar.toLowerCase()) {
            // Lowercase → uppercase: new word boundary
            words.push(current.toLowerCase());
            current = ch;
          } else if (next && next === next.toLowerCase() && next !== "") {
            // End of acronym, about to switch to lowercase (e.g., "XMLParser")
            words.push(current.toLowerCase());
            current = ch;
          } else {
            // Continue acronym run (e.g., "HTTP")
            current += ch;
          }
        } else {
          current = ch;
        }
      } else {
        // Lowercase letter — just accumulate
        current += ch;
      }
    } else if (/[0-9]/.test(ch)) {
      // Digit: append to current word or start a new one
      current += ch;
    } else {
      // Other character: flush and skip
      if (current) {
        words.push(current.toLowerCase());
        current = "";
      }
    }
  }

  if (current) {
    words.push(current.toLowerCase());
  }

  return words.filter((w) => w.length > 0);
}

// ─── Word validation ───────────────────────────────────────────

/**
 * Minimal English dictionary of common programming and general words.
 * In production this would be replaced by a full dictionary or external API.
 */
const builtInDictionary = new Set([
  // Common programming terms
  "abstract", "access", "action", "active", "actual", "adapter", "add",
  "adjustment", "admin", "advanced", "after", "again", "against", "algorithm",
  "align", "all", "allow", "almost", "along", "already", "also", "alt",
  "always", "amendment", "among", "amount", "anchor", "and", "angle",
  "animation", "annotation", "announce", "another", "answer", "any",
  "anybody", "anymore", "anyone", "anything", "anyway", "anywhere",
  "appear", "append", "apply", "appropriate", "approval", "area",
  "arg", "argument", "arguments", "args", "arithmetic", "array", "arrange",
  "arrive", "arrow", "as", "aside", "ask", "aspect", "assert", "assign",
  "async", "associate", "assume", "at", "attach", "attack", "attempt",
  "attention", "attr", "attribute", "attributes", "audio", "auth",
  "author", "auto", "automatic", "automatically", "autoplay", "available",
  "away", "awkward",
  // B - continue building out common words
  "back", "background", "bad", "bag", "balance", "ball", "band", "bar",
  "base", "based", "baseline", "basic", "basically", "basis", "batch",
  "be", "bear", "beat", "beautiful", "because", "become", "bed", "before",
  "begin", "beginning", "behalf", "behave", "behavior", "behind", "being",
  "believe", "bell", "below", "belt", "bench", "benchmark", "benefit",
  "best", "bet", "better", "between", "beyond", "bias", "bid", "big",
  "bike", "bill", "bind", "binding", "bio", "bit", "bite", "black",
  "blank", "blanket", "block", "blog", "blow", "blue", "board", "boat",
  "body", "bomb", "bond", "bone", "bonus", "book", "bookmark", "boot",
  "border", "borrow", "boss", "both", "bother", "bottle", "bottom",
  "bound", "boundary", "box", "boy", "brain", "branch", "brand", "brave",
  "bread", "break", "breakpoint", "breakthrough", "breath", "breathe",
  "breathing", "brick", "bridge", "brief", "briefly", "bright",
  "bring", "broad", "broken", "brother", "brown", "brush", "bubble",
  "bucket", "budget", "buffer", "bug", "build", "builder", "building",
  "bulk", "bullet", "bundle", "burden", "burn", "burst", "bus", "bush",
  "business", "busy", "button", "buy", "buyer", "by", "byte",
  // C - common words
  "cache", "caching", "call", "callback", "camera", "camp", "campaign",
  "can", "cancel", "capacity", "capable", "capital", "capture", "car",
  "card", "care", "career", "careful", "carefully", "carry", "cart",
  "case", "cash", "cast", "cat", "catch", "category", "cause", "caution",
  "cell", "center", "central", "centre", "ceremony", "chain", "chair",
  "chairman", "challenge", "chamber", "champion", "chance", "change",
  "channel", "chapter", "character", "characteristic", "charge", "chart",
  "chat", "check", "cheese", "chef", "chemical", "chest", "chicken",
  "chief", "child", "children", "choice", "choose", "chosen", "chunk",
  "church", "circle", "circumstance", "cite", "citation", "city", "civil",
  "claim", "class", "classic", "classify", "clean", "clear", "click",
  "client", "climate", "climb", "clock", "close", "closely", "closer",
  "cloth", "cloud", "club", "cluster", "coach", "coal", "coast", "coat",
  "code", "coffee", "cognitive", "coin", "cold", "collapse", "collaborate",
  "colleague", "collect", "collection", "college", "color", "column",
  "combine", "come", "comedy", "comfort", "command", "comment", "commit",
  "committee", "common", "communicate", "communication", "community",
  "company", "compare", "comparison", "compete", "competition", "competitor",
  "complaint", "complete", "completely", "complex", "component",
  "compose", "composition", "comprehensive", "computer", "concentrate",
  "concept", "concern", "concert", "conclude", "conclusion", "condition",
  "conduct", "conference", "confidence", "confirm", "conflict", "conform",
  "connect", "connection", "conscious", "consensus", "consider",
  "considerable", "consideration", "consist", "consistent", "constant",
  "constitute", "construct", "construction", "consult", "consume",
  "consumer", "contact", "contain", "container", "contemporary",
  "content", "context", "continue", "contract", "contrary", "contrast",
  "contribute", "contribution", "control", "convenience", "convention",
  "conversation", "convert", "convince", "cook", "cookie", "cool",
  "cooperate", "cope", "copy", "core", "corn", "corner", "corporate",
  "correct", "correspond", "cost", "cotton", "could", "count",
  "counter", "country", "county", "couple", "courage", "course", "court",
  "cousin", "cover", "coverage", "create", "creation", "creative",
  "creator", "creature", "credit", "crew", "crime", "criminal", "crop",
  "cross", "crowd", "crucial", "cry", "culture", "cup", "curl", "current",
  "curriculum", "custom", "customer", "cut", "cycle",
  // D - common words
  "daily", "damage", "dance", "danger", "dare", "dark", "data", "database",
  "date", "daughter", "day", "dead", "deal", "dealer", "dear", "death",
  "debate", "debt", "decade", "decide", "decision", "deck", "declare",
  "decline", "decorate", "decrease", "deep", "deeply", "deer", "defeat",
  "defend", "defense", "deficit", "define", "definition", "degree", "delay",
  "delegate", "delete", "demand", "democracy", "demonstrate", "department",
  "depend", "dependent", "depending", "deploy", "deposit", "depression",
  "depth", "deputy", "derive", "describe", "description", "desert",
  "design", "designer", "desire", "desk", "despite", "destination",
  "destroy", "detail", "detailed", "detect", "determine", "develop",
  "developer", "development", "device", "devote", "dialog", "dialogue",
  "diamond", "did", "die", "diet", "differ", "difference", "different",
  "differently", "difficult", "difficulty", "dig", "digital", "dimension",
  "dining", "dinner", "direct", "direction", "director", "dirt", "dirty",
  "disabled", "disagree", "disappear", "disaster", "discipline",
  "disclose", "discount", "discover", "discovery", "discuss", "discussion",
  "disease", "dish", "dismiss", "display", "dispose", "distance",
  "distant", "distinct", "distinction", "distinguish", "distribute",
  "distribution", "district", "diverse", "divide", "division", "divorce",
  "do", "doctor", "document", "dog", "doing", "domestic", "dominant",
  "domain", "done", "door", "double", "doubt", "down", "downtown", "due",
  "during", "duty",
  // E - common words
  "each", "ear", "early", "earn", "earth", "ease", "east", "easy",
  "eat", "economic", "economy", "edge", "edit", "editor", "education",
  "effect", "effective", "efficiency", "efficient", "effort", "eight",
  "either", "elderly", "elect", "election", "electric", "element",
  "eliminate", "elite", "else", "elsewhere", "email", "embrace",
  "emerge", "emergency", "emotion", "emphasis", "employ", "employee",
  "employer", "employment", "empty", "enable", "encounter", "encourage",
  "end", "enemy", "energy", "enforce", "engage", "engine", "engineer",
  "engineering", "enhance", "enjoy", "enormous", "enough", "ensure",
  "enter", "enterprise", "entertainment", "entire", "entirely", "entity",
  "entrance", "entry", "environment", "episode", "equal", "equally",
  "equipment", "error", "escape", "especially", "essay", "essential",
  "essentially", "establish", "estate", "estimate", "even", "evening",
  "event", "eventually", "ever", "every", "everybody", "everyone",
  "everything", "everywhere", "evidence", "evil", "exact", "exactly",
  "examination", "examine", "example", "exceed", "excellent", "except",
  "exception", "exchange", "exciting", "executive", "exercise", "exhibit",
  "exist", "existence", "existing", "exit", "expand", "expect",
  "expenditure", "expense", "experience", "experiment", "expert",
  "explain", "explanation", "explore", "explosion", "export", "expose",
  "exposure", "express", "expression", "extend", "extension", "extensive",
  "extent", "external", "extra", "extract", "extreme", "extremely",
  // F - common words
  "fabric", "face", "facility", "fact", "factor", "factory", "faculty",
  "fade", "fail", "failure", "fair", "faith", "fall", "false", "familiar",
  "family", "famous", "fan", "fantasy", "far", "farm", "farmer", "fashion",
  "fast", "fat", "fate", "father", "fault", "favor", "favorite", "fear",
  "feature", "feed", "feedback", "feel", "feeling", "fellow", "female",
  "fence", "few", "field", "fight", "figure", "file", "fill", "film",
  "final", "finally", "finance", "financial", "find", "finding", "fine",
  "finger", "finish", "fire", "firm", "first", "fish", "fishing", "fit",
  "five", "fix", "flag", "flame", "flash", "flat", "flavor", "flee",
  "flesh", "flight", "float", "floor", "flow", "flower", "fly", "focus",
  "follow", "following", "food", "foot", "football", "for", "force",
  "foreign", "forest", "forever", "forget", "form", "formal", "format",
  "formation", "former", "formula", "forth", "fortune", "forward",
  "found", "foundation", "four", "frame", "framework", "free", "freedom",
  "frequently", "fresh", "friend", "friendly", "friendship", "from",
  "front", "fruit", "fuel", "full", "fully", "function", "fund", "fundamental",
  "funding", "funeral", "funny", "furniture", "furthermore", "future",
  // G - common words
  "gain", "galaxy", "gallery", "game", "gang", "gap", "garage", "garden",
  "garlic", "gas", "gate", "gather", "gear", "gender", "gene", "general",
  "generally", "generate", "generation", "genetic", "genre", "gentle",
  "gentleman", "genuine", "get", "ghost", "giant", "gift", "girl",
  "give", "given", "glad", "glance", "glass", "global", "glove", "go",
  "goal", "god", "gold", "golden", "golf", "good", "government", "governor",
  "grab", "grace", "grade", "gradually", "graduate", "grain", "grand",
  "grandfather", "grandmother", "grant", "grass", "grave", "gray", "great",
  "green", "grocery", "ground", "group", "grow", "growth", "guarantee",
  "guard", "guess", "guest", "guide", "guideline", "guilty", "guitar",
  "gun", "guy",
  // H - common words
  "habit", "habitat", "hair", "half", "hall", "hand", "handle", "hang",
  "happen", "happy", "hard", "hardly", "hat", "hate", "have", "he",
  "head", "headline", "header", "health", "healthy", "hear", "hearing",
  "heart", "heat", "heavy", "heel", "height", "hello", "help", "helpful",
  "her", "here", "heritage", "hero", "herself", "hide", "high", "highlight",
  "highly", "highway", "hill", "him", "himself", "hint", "hip", "hire",
  "his", "historian", "historic", "history", "hit", "hold", "hole", "holiday",
  "holy", "home", "homework", "honest", "honey", "honor", "hope", "horizon",
  "horror", "horse", "hospital", "host", "hot", "hotel", "hour", "house",
  "household", "housing", "how", "however", "huge", "human", "humor", "hundred",
  "hungry", "hunt", "hunter", "hurricane", "hurry", "hurt", "husband",
  // I - common words
  "I", "ice", "idea", "ideal", "identify", "identity", "ideology", "if",
  "ignore", "ill", "illegal", "illness", "illustrate", "image", "imagination",
  "imagine", "immediate", "immediately", "immigrant", "impact", "implement",
  "implication", "imply", "importance", "important", "impose", "impossible",
  "impress", "impression", "improve", "in", "incentive", "incident",
  "include", "including", "income", "incorporate", "increase", "indeed",
  "independence", "independent", "index", "indicate", "individual",
  "industrial", "industry", "infant", "infection", "inflation", "influence",
  "inform", "information", "ingredient", "initial", "initiative", "injury",
  "inner", "innocent", "innovation", "input", "inquiry", "insert",
  "inside", "insight", "insist", "inspect", "inspiration", "install",
  "instance", "instead", "institution", "instruction", "instrument",
  "insurance", "integrate", "intelligence", "intend", "intense",
  "intention", "interact", "interaction", "interest", "interested",
  "interesting", "internal", "international", "internet", "interpret",
  "intervention", "interview", "into", "introduce", "introduction",
  "investigate", "investment", "invite", "involve", "iron", "island",
  "issue", "it", "item", "its", "itself",
  // J - common words
  "job", "join", "joint", "joke", "journal", "journey", "joy", "judge",
  "juice", "jump", "junior", "jury", "just", "justice", "justify",
  // K - common words
  "keep", "key", "keyboard", "kick", "kid", "kill", "kind", "king",
  "kitchen", "knee", "knife", "knock", "know", "knowledge",
  // L - common words
  "label", "labor", "lack", "ladder", "lady", "lake", "land", "landscape",
  "language", "lap", "large", "largely", "last", "late", "later", "latest",
  "laugh", "launch", "law", "lawn", "lawsuit", "layer", "lead", "leader",
  "leadership", "leading", "leaf", "league", "lean", "learn", "learning",
  "least", "leather", "leave", "lecture", "left", "leg", "legal", "legend",
  "legislation", "legitimate", "lemon", "length", "less", "lesson", "let",
  "letter", "level", "liberal", "library", "license", "lid", "lie", "life",
  "lifestyle", "lifetime", "lift", "light", "like", "likely", "limit",
  "limited", "line", "link", "lion", "lip", "list", "listen", "literally",
  "literary", "literature", "little", "live", "living", "load", "loan",
  "local", "locate", "location", "lock", "log", "logic", "long", "look",
  "loose", "lose", "loss", "lost", "lot", "loud", "love", "lovely",
  "lover", "low", "lower", "luck", "lucky", "lunch", "luxury",
  // M - common words
  "machine", "mad", "magazine", "magic", "mail", "main", "mainly",
  "maintain", "major", "maker", "makeup", "male", "mall", "man",
  "manage", "manager", "management", "mandate", "manner", "manufacturer",
  "many", "map", "mapping", "margin", "mark", "market", "marketing",
  "marriage", "married", "mask", "mass", "master", "match", "material",
  "math", "matter", "maximum", "may", "maybe", "mayor", "meal", "mean",
  "meaning", "means", "meanwhile", "measure", "measurement", "meat",
  "mechanism", "media", "medical", "medication", "medicine", "medium",
  "meet", "meeting", "member", "membership", "memory", "mental", "mention",
  "menu", "mere", "message", "metal", "meter", "method", "middle", "might",
  "military", "milk", "mind", "mine", "minister", "minor", "minute",
  "miracle", "mirror", "miss", "mission", "mistake", "mix", "mixture",
  "mode", "model", "moderate", "modern", "modest", "module", "mom",
  "moment", "money", "monitor", "month", "mood", "moon", "moral", "more",
  "morning", "mortgage", "most", "mostly", "mother", "motion", "motivate",
  "motor", "mount", "mountain", "mouse", "mouth", "move", "movement",
  "movie", "much", "multiple", "murder", "muscle", "museum", "music",
  "must", "mutual", "my", "myself",
  // N - common words
  "nail", "name", "narrative", "narrow", "nation", "national", "native",
  "natural", "nature", "near", "nearby", "nearly", "neat", "necessary",
  "necessity", "neck", "need", "negative", "negotiate", "neighbor",
  "neighborhood", "neither", "nerve", "nest", "net", "network", "never",
  "nevertheless", "new", "news", "next", "nice", "night", "nine", "no",
  "nobody", "node", "noise", "nominee", "none", "nonetheless", "nor",
  "normal", "normally", "north", "northern", "nose", "not", "note",
  "nothing", "notice", "notion", "novel", "now", "nowhere", "nuclear",
  "number", "numerous", "nurse", "nut",
  // O - common words
  "objective", "obligation", "observation", "observe", "obtain", "obvious",
  "occasion", "occupy", "occur", "ocean", "odd", "of", "off", "offense",
  "offensive", "offer", "office", "officer", "official", "often", "oh",
  "oil", "ok", "old", "olive", "olympic", "on", "once", "one", "ongoing",
  "online", "only", "onto", "open", "opening", "operate", "operation",
  "operator", "opinion", "opponent", "opportunity", "oppose", "option",
  "or", "orange", "order", "ordinary", "organ", "organic", "organization",
  "orient", "origin", "original", "other", "otherwise", "ought", "our",
  "ourselves", "out", "outcome", "outdoor", "outer", "outlet", "output",
  "outside", "oven", "over", "overall", "overcome", "overlook", "owe",
  "own", "owner", "ownership", "oxygen",
  // P - common words
  "pace", "pack", "package", "packet", "pad", "page", "pain", "paint",
  "pair", "palace", "pale", "palm", "panel", "panic", "paper", "parent",
  "park", "part", "participant", "participate", "particular", "particularly",
  "partner", "party", "pass", "passage", "passenger", "passion", "past",
  "patch", "path", "patience", "patient", "pattern", "pause", "pay",
  "payment", "peace", "peak", "peer", "penalty", "pencil", "people",
  "pepper", "per", "perceive", "percent", "percentage", "perfect",
  "perform", "performance", "perhaps", "period", "permanent", "permission",
  "permit", "person", "personal", "personality", "personnel", "perspective",
  "persuade", "pet", "phase", "phenomenon", "philosophy", "phone",
  "photo", "photograph", "phrase", "physical", "physician", "physics",
  "piano", "pick", "picture", "piece", "pile", "pilot", "pin", "pink",
  "pipe", "pitch", "place", "plan", "plane", "planet", "planning", "plant",
  "plastic", "plate", "platform", "play", "player", "plea", "pleasant",
  "please", "pleasure", "plenty", "plot", "plug", "plus", "pocket",
  "poem", "poet", "point", "pole", "police", "policy", "political",
  "politician", "politics", "poll", "pollution", "pool", "poor", "pop",
  "popular", "population", "porch", "port", "portion", "portrait",
  "pose", "position", "positive", "possess", "possession", "possible",
  "possibly", "post", "pot", "potential", "pound", "pour", "poverty",
  "powder", "power", "powerful", "practice", "practical", "pray", "prayer",
  "precisely", "predict", "prefer", "preference", "pregnancy", "pregnant",
  "premier", "premise", "premium", "preparation", "prepare", "presence",
  "present", "presentation", "preserve", "president", "press", "pressure",
  "pretend", "pretty", "prevent", "previous", "price", "pride", "primary",
  "prime", "principal", "principle", "print", "prior", "priority",
  "prison", "private", "privilege", "prize", "problem", "procedure",
  "proceed", "process", "produce", "producer", "product", "production",
  "profession", "professional", "professor", "profile", "profit", "program",
  "progress", "project", "promise", "promote", "prompt", "proof",
  "proper", "property", "proportion", "proposal", "propose", "proposed",
  "prosecutor", "prospect", "protect", "protein", "protest", "proud",
  "prove", "provide", "province", "provision", "psychological", "psychology",
  // Q - common words
  "public", "pull", "punish", "purpose", "push", "put",
  // R - common words
  "race", "racial", "rack", "radical", "radio", "rail", "rain", "raise",
  "range", "rank", "rapid", "rare", "rate", "rather", "rating", "ratio",
  "raw", "reach", "react", "reaction", "read", "reader", "reading", "ready",
  "real", "reality", "realize", "really", "reason", "reasonable", "recall",
  "receive", "recent", "recipe", "recognize", "recommend", "record",
  "recover", "recruit", "red", "reduce", "refer", "reference", "reflect",
  "reform", "refugee", "refuse", "regard", "region", "register", "regular",
  "regulation", "reject", "relate", "relation", "relationship", "relative",
  "relatively", "relax", "release", "relevant", "relief", "religion",
  "religious", "rely", "remain", "remarkable", "remember", "remind",
  "remote", "remove", "rent", "repair", "repeat", "replace", "reply",
  "report", "represent", "representative", "reputation", "request",
  "require", "research", "reserve", "resident", "resist", "resolution",
  "resolve", "resort", "resource", "respect", "respond", "response",
  "responsibility", "responsible", "rest", "restaurant", "restore",
  "result", "retain", "retire", "return", "reveal", "revenue", "review",
  "revolution", "reward", "rhythm", "rice", "rich", "rid", "ride",
  "rifle", "right", "ring", "rise", "risk", "river", "road", "rob",
  "rock", "role", "roll", "romantic", "roof", "room", "root", "rope",
  "rose", "rough", "round", "route", "routine", "row", "royal", "rub",
  "rule", "ruler", "rumor", "run", "running", "rural", "rush", "rust",
  // S - common words
  "safe", "safety", "said", "sail", "salad", "salary", "sale", "salt",
  "same", "sample", "sanction", "sand", "satellite", "satisfaction",
  "satisfy", "sauce", "save", "saving", "say", "scale", "scandal",
  "scenario", "scene", "schedule", "scheme", "scholar", "school", "science",
  "scope", "score", "screen", "script", "sea", "search", "season", "seat",
  "second", "secondary", "secret", "section", "sector", "secure", "security",
  "see", "seed", "seek", "seem", "segment", "seize", "select", "selection",
  "self", "sell", "senate", "senator", "send", "senior", "sense", "sensitive",
  "sentence", "separate", "sequence", "series", "serious", "serve",
  "service", "session", "set", "setting", "settle", "seven", "several",
  "severe", "sex", "sexual", "shade", "shadow", "shake", "shall", "shame",
  "shape", "share", "sharp", "she", "sheet", "shelf", "shell", "shelter",
  "shift", "shine", "ship", "shirt", "shock", "shoe", "shoot", "shop",
  "shopping", "shore", "short", "shot", "should", "shoulder", "shout",
  "show", "shower", "shrug", "shut", "sick", "side", "sight", "sign",
  "signal", "significant", "silence", "silent", "silk", "silver", "similar",
  "simple", "simply", "simulate", "sin", "since", "sing", "single", "sink",
  "sir", "sister", "sit", "site", "situation", "six", "size", "ski",
  "skill", "skin", "sky", "sleep", "slice", "slide", "slight", "slightly",
  "slip", "slow", "small", "smart", "smell", "smile", "smoke", "smooth",
  "snap", "snow", "so", "soap", "soccer", "social", "society", "soft",
  "software", "soil", "solar", "soldier", "solid", "solution", "solve",
  "some", "somebody", "somehow", "someone", "something", "sometimes",
  "somewhat", "somewhere", "son", "song", "soon", "sophisticated", "sort",
  "soul", "sound", "soup", "source", "south", "southern", "space", "spare",
  "speak", "speaker", "special", "specialist", "species", "specific",
  "speech", "speed", "spell", "spend", "sphere", "spice", "spin", "spirit",
  "spiritual", "split", "spokesman", "sport", "spot", "spread", "spring",
  "square", "stable", "stack", "staff", "stage", "stair", "stake", "stand",
  "standard", "standing", "star", "stare", "start", "state", "statement",
  "station", "statistic", "status", "stay", "steady", "steal", "steam",
  "steel", "step", "stick", "still", "stimulus", "stir", "stock", "stomach",
  "stone", "stop", "storage", "store", "storm", "story", "straight",
  "strange", "stranger", "strategy", "stream", "street", "strength",
  "stress", "stretch", "strike", "string", "strip", "stroke", "strong",
  "structure", "struggle", "student", "studio", "study", "stuff", "stupid",
  "style", "subject", "submit", "subsequent", "substance", "such", "sudden",
  "suffer", "sufficient", "suggest", "suicide", "suit", "summer", "summit",
  "sun", "super", "supply", "support", "suppose", "sure", "surface",
  "surgeon", "surgery", "surprise", "surround", "survey", "survival",
  "survive", "suspect", "suspend", "sustain", "swear", "sweat", "sweep",
  "sweet", "swim", "swing", "switch", "sword", "symbol", "symptom",
  // T - common words
  "system", "table", "tablet", "tackle", "tailor", "take", "tale",
  "talent", "talk", "tall", "tank", "tap", "tape", "target", "task",
  "taste", "tax", "tea", "teach", "teacher", "team", "tear", "technical",
  "technique", "technology", "teenager", "telephone", "television",
  "tell", "temperature", "temporary", "ten", "tenant", "tend", "term",
  "terms", "terrible", "territory", "terror", "test", "text", "than",
  "thank", "that", "the", "theater", "their", "them", "theme", "themselves",
  "then", "theory", "therapy", "there", "therefore", "these", "they",
  "thick", "thin", "thing", "think", "thinking", "third", "thirsty",
  "this", "those", "though", "thought", "thread", "threat", "three",
  "throat", "through", "throw", "thus", "ticket", "tie", "tight", "time",
  "tiny", "tip", "tire", "tired", "title", "to", "tobacco", "today",
  "toe", "together", "tomato", "tomorrow", "tone", "tongue", "tonight",
  "too", "tool", "tooth", "top", "topic", "total", "totally", "touch",
  "tough", "tour", "tourist", "tournament", "toward", "towards", "tower",
  "town", "toy", "trace", "track", "trade", "tradition", "traffic",
  "tragedy", "trail", "train", "training", "transfer", "transform",
  "transition", "translate", "transmission", "transport", "trap",
  "travel", "treat", "treatment", "tremendous", "trend", "trial", "tribe",
  "trick", "trip", "troop", "trouble", "truck", "true", "truly", "trust",
  "truth", "try", "tube", "tune", "turn", "twelve", "twenty", "twice",
  "twin", "two", "type",
  // U - common words
  "ugly", "ultimate", "unable", "uncle", "under", "undergo", "understand",
  "understanding", "unemployment", "unexpected", "unfair", "unfold",
  "unhappy", "uniform", "union", "unique", "unit", "unite", "universal",
  "universe", "university", "unknown", "unless", "unlike", "unlikely",
  "until", "unusual", "up", "upon", "upper", "urban", "urge", "us",
  "use", "used", "useful", "user", "usually", "utility",
  // V - common words
  "vacation", "valley", "valuable", "value", "variable", "variation",
  "variety", "various", "vary", "vast", "vehicle", "venture", "version",
  "versus", "very", "vessel", "veteran", "via", "victim", "victory",
  "video", "view", "viewer", "village", "violence", "virtual", "virtually",
  "virtue", "virus", "visible", "vision", "visit", "visitor", "visual",
  "vital", "voice", "volume", "volunteer", "vote", "voter", "vs",
  // W - common words
  "wait", "wake", "walk", "wall", "wander", "want", "war", "warm",
  "warn", "wash", "waste", "watch", "water", "wave", "way", "we",
  "weakness", "wealth", "weapon", "wear", "weather", "web", "wedding",
  "week", "weekly", "weigh", "weight", "welcome", "welfare", "well",
  "west", "western", "wet", "what", "whatever", "wheel", "when",
  "whenever", "where", "whereas", "wherever", "whether", "which",
  "while", "whisper", "white", "who", "whole", "whom", "whose", "why",
  "wide", "widely", "widespread", "wife", "wild", "will", "willing",
  "win", "wind", "window", "wine", "wing", "winner", "winter", "wipe",
  "wire", "wisdom", "wise", "wish", "with", "withdraw", "within",
  "without", "witness", "woman", "wonder", "wonderful", "wood", "word",
  "work", "worker", "working", "works", "workshop", "world", "worried",
  "worry", "worth", "would", "wound", "wrap", "write", "writer",
  "writing", "wrong",
  // X - Y - Z
  "yacht", "yard", "year", "yellow", "yes", "yesterday", "yet", "yield",
  "you", "young", "your", "yours", "yourself", "youth", "zone",
]);

/**
 * Check whether a single lowercase word is considered valid (present in the
 * dictionary or explicitly ignored). Returns true if the word passes.
 */
export function checkWord(word: string, ignoreList: Set<string>): boolean {
  const lower = word.toLowerCase().trim();
  if (!lower) return true;

  // Single characters and short fragments are not checked
  if (lower.length <= 1) return true;

  // All-digit strings are valid
  if (/^\d+$/.test(lower)) return true;

  // Mixed alphanumeric tokens (like "v2", "a1b") are skipped
  if (/[a-z]/.test(lower) && /\d/.test(lower)) return true;

  // Check ignore list first, then dictionary
  return ignoreList.has(lower) || builtInDictionary.has(lower);
}

// ─── Suggestions ───────────────────────────────────────────────

/**
 * Compute Levenshtein distance between two strings.
 */
function levenshtein(a: string, b: string): number {
  const m = a.length;
  const n = b.length;

  if (m === 0) return n;
  if (n === 0) return m;

  // Use two-row optimization for memory
  let prev: number[] = [];
  for (let i = 0; i <= n; i++) prev[i] = i;
  let curr: number[] = new Array(n + 1);

  for (let i = 1; i <= m; i++) {
    curr[0] = i;
    for (let j = 1; j <= n; j++) {
      const cost = a[i - 1] === b[j - 1] ? 0 : 1;
      curr[j] = Math.min(
        prev[j] + 1,       // deletion
        curr[j - 1] + 1,   // insertion
        prev[j - 1] + cost, // substitution
      );
    }
    // Swap references: curr becomes prev for next iteration
    const temp = prev;
    prev = curr;
    curr = temp;
  }

  return prev[n];
}

/**
 * Return up to `maxSuggestions` dictionary words that are closest to the
 * misspelled word by Levenshtein distance. Only returns candidates within
 * a reasonable edit distance threshold.
 */
export function getSuggestions(
  word: string,
  maxSuggestions = 5,
): string[] {
  const lower = word.toLowerCase().trim();

  // Collect all candidates and score them
  type Candidate = { word: string; distance: number };
  const candidates: Candidate[] = [];

  for (const dictWord of builtInDictionary) {
    // Skip words that are very different in length (optimization)
    if (Math.abs(dictWord.length - lower.length) > 3) continue;

    const dist = levenshtein(lower, dictWord);
    if (dist <= 2 && dist > 0) {
      candidates.push({ word: dictWord, distance: dist });
    }
  }

  // Sort by distance, then alphabetically for stability
  candidates.sort((a, b) => {
    if (a.distance !== b.distance) return a.distance - b.distance;
    return a.word.localeCompare(b.word);
  });

  return candidates.slice(0, maxSuggestions).map((c) => c.word);
}
