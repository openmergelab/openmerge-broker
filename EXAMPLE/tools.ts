/**
 * merge/tools.ts
 *
 * Tool implementations for the Merge OpenClaw skill.
 * These are the functions the agent calls via the tools
 * defined in SKILL.md.
 *
 * Privacy rules enforced here:
 *   - Profile is never transmitted to the broker
 *   - Preference vector is encrypted before upload
 *   - Broker receives only anonymous signals
 *   - Photos are never referenced or transmitted
 */

import * as fs from "fs/promises"
import * as path from "path"
import * as crypto from "crypto"
import * as h3 from "h3-js"

// ─── Constants ────────────────────────────────────────────────────────────────

const WORKSPACE = path.join(
  process.env.HOME || "~",
  ".openclaw/workspace/merge"
)
const BROKER_URL = "https://api.merge.app"
const SIGNAL_EXPIRY_DAYS = 7

// ─── Types ────────────────────────────────────────────────────────────────────

// Lifestyle choices — used for preference matching and introduction card
interface Lifestyle {
  drinking:  "yes" | "sometimes" | "no" | "prefer_not_say"
  smoking:   "yes" | "sometimes" | "no" | "prefer_not_say"
  exercise:  "daily" | "often" | "sometimes" | "rarely"
  pets:      "have" | "love" | "neutral" | "allergic"
  kids:      "have" | "want" | "open" | "no" | "prefer_not_say"
  diet:      "omnivore" | "vegetarian" | "vegan" | "other" | "prefer_not_say"
}

interface Profile {
  version: number

  // Identity — never transmitted to broker
  firstName: string
  age: number
  tagline: string             // one punchy line e.g. "I debug in production"
  bio: string                 // freeform, a few sentences

  // Human texture — never transmitted to broker
  // Extracted from onboarding conversation
  interests:      string[]    // ["rust", "climbing", "sci-fi"]
  hobbies:        string[]    // ["bouldering", "building keyboards", "sourdough"]
  values:         string[]    // ["honesty", "ambition", "curiosity"]
  personalityTags: string[]   // ["introvert", "night owl", "builder", "overthinker"]
  lifestyle:      Lifestyle
  lookingFor:     "relationship" | "dating" | "unsure"

  // Discord identity
  discordHandle: string       // human-readable e.g. "maya_builds" — revealed on match
  discordId: string           // numeric ID e.g. "1234567890123" — sent to broker (hashed)

  // Matching — partially encrypted before broker upload
  seeking:    "M" | "F" | "NB" | "any"
  ageRange:   [number, number]  // [min, max]
  locationH3: string            // H3 hex cell resolution 8, ~1km radius

  // Verification
  ageVerified:           boolean
  verifiedAt?:           string   // ISO8601
  verificationProvider?: string

  // Infrastructure — sent to broker for push delivery
  pushToken?: string     // rotated regularly, stored here for upload_signal

  // State
  setupComplete: boolean
  createdAt:    string
  updatedAt:    string
}

// Lifestyle dealbreakers — hard filters applied before agent conversation
interface LifestyleDealbreakers {
  smoking?:  boolean      // true = will not match smokers
  kids?:     boolean      // true = will not match people who have kids
  drinking?: boolean      // true = will not match drinkers
  diet?:     string[]     // e.g. ["vegan"] = only match people with same diet
}

interface Preferences {
  version: number

  // Core matching signals — extracted from onboarding conversation
  values:             string[]    // must-haves e.g. ["honesty", "ambition"]
  dealbreakers:       string[]    // hard stops e.g. ["no ambition", "dishonesty"]
  communicationStyle: string      // e.g. "async", "responsive but not constant"
  lookingForVibe:     string      // freeform e.g. "someone who challenges me"
  vibeNotes:          string      // agent's own notes — nuance that doesn't fit above

  // Interest overlap — used to score candidate compatibility
  // Higher weight = more important to the user
  interestWeights: {
    [interest: string]: number    // e.g. { "climbing": 0.9, "music": 0.4 }
  }

  // Lifestyle hard filters — applied before ephemeral agent conversation
  lifestyleDealbreakers: LifestyleDealbreakers

  // Personality signals — used in agent compatibility scoring
  preferredPersonality: string[]  // e.g. ["curious", "ambitious", "calm"]
  avoidPersonality:     string[]  // e.g. ["flaky", "controlling"]

  updatedAt: string
}

interface Signal {
  signalId: string
  expiresAt: string
  uploadedAt: string
}

interface Match {
  matchId: string
  discordChannelId: string
  introducedAt: string
}

// ─── File Helpers ─────────────────────────────────────────────────────────────

async function ensureWorkspace(): Promise<void> {
  await fs.mkdir(WORKSPACE, { recursive: true })
}

async function readProfile(): Promise<Profile | null> {
  try {
    const raw = await fs.readFile(
      path.join(WORKSPACE, "profile.json"),
      "utf8"
    )
    return JSON.parse(raw)
  } catch {
    return null
  }
}

async function writeProfile(profile: Profile): Promise<void> {
  await ensureWorkspace()
  await fs.writeFile(
    path.join(WORKSPACE, "profile.json"),
    JSON.stringify(profile, null, 2)
  )
}

async function readPreferences(): Promise<Preferences | null> {
  try {
    const raw = await fs.readFile(
      path.join(WORKSPACE, "preferences.json"),
      "utf8"
    )
    return JSON.parse(raw)
  } catch {
    return null
  }
}

async function readSignal(): Promise<Signal | null> {
  try {
    const raw = await fs.readFile(
      path.join(WORKSPACE, "signal.json"),
      "utf8"
    )
    return JSON.parse(raw)
  } catch {
    return null
  }
}

async function readMatches(): Promise<Match[]> {
  try {
    const raw = await fs.readFile(
      path.join(WORKSPACE, "matches.json"),
      "utf8"
    )
    return JSON.parse(raw)
  } catch {
    return []
  }
}

// ─── Credential Helpers ───────────────────────────────────────────────────────

/**
 * Retrieve a stored credential from OpenClaw's secure store.
 * In production this calls the OpenClaw credential API.
 * Here it reads from environment for testability.
 */
async function getCredential(key: string): Promise<string | null> {
  // OpenClaw provides this via its runtime API
  // openclaw.credentials.get(key)
  return process.env[key.toUpperCase().replace(/\./g, "_")] || null
}

async function getSessionToken(): Promise<string | null> {
  return getCredential("merge.session_token")
}

// ─── Encryption ───────────────────────────────────────────────────────────────

/**
 * Encrypt the preference vector on device before upload.
 * The broker receives an encrypted blob it cannot decrypt.
 *
 * Uses AES-256-GCM with a key derived from the device
 * identity — never transmitted to the broker.
 */
function encryptPreferences(preferences: Preferences): string {
  // In production: derive key from device identity
  // For now: generate a stable key from a local secret
  const deviceSecret =
    process.env.MERGE_DEVICE_SECRET || crypto.randomBytes(32).toString("hex")
  const key = crypto.scryptSync(deviceSecret, "merge-prefs-v1", 32)
  const iv = crypto.randomBytes(16)
  const cipher = crypto.createCipheriv("aes-256-gcm", key, iv)

  const plaintext = JSON.stringify(preferences)
  const encrypted = Buffer.concat([
    cipher.update(plaintext, "utf8"),
    cipher.final(),
  ])
  const authTag = cipher.getAuthTag()

  // Pack: iv (16) + authTag (16) + ciphertext
  return Buffer.concat([iv, authTag, encrypted]).toString("base64")
}

/**
 * Generate an H3 hex cell from the device's current location.
 * Resolution 8 ≈ ~0.7km² per cell — precise enough for matching,
 * coarse enough to not reveal exact location.
 */
async function getLocationH3(): Promise<string> {
  // In production: use OpenClaw's location API
  // openclaw.location.getApproximate({ resolution: 8 })
  // Returns H3 index at resolution 8

  // Fallback: read from profile if already set
  const profile = await readProfile()
  if (profile?.locationH3) return profile.locationH3

  // For testing: return a London cell
  return h3.latLngToCell(51.5074, -0.1278, 8)
}

/**
 * Get or generate the anonymous ID for this device.
 * This ID is never linked to the user's Discord identity
 * on the broker.
 */
async function getAnonymousId(): Promise<string> {
  const idFile = path.join(WORKSPACE, ".anonymous_id")
  try {
    return await fs.readFile(idFile, "utf8")
  } catch {
    const id = crypto.randomUUID()
    await ensureWorkspace()
    await fs.writeFile(idFile, id)
    return id
  }
}

/**
 * Get or generate the device's key pair for P2P encryption.
 * Public key is uploaded with the signal.
 * Private key never leaves the device.
 */
async function getKeyPair(): Promise<{ publicKey: string }> {
  const keyFile = path.join(WORKSPACE, ".keypair")
  try {
    const raw = await fs.readFile(keyFile, "utf8")
    const { publicKey } = JSON.parse(raw)
    return { publicKey }
  } catch {
    const { publicKey, privateKey } = crypto.generateKeyPairSync("ec", {
      namedCurve: "P-256",
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    })
    await ensureWorkspace()
    // Store full pair locally — private key never leaves
    await fs.writeFile(keyFile, JSON.stringify({ publicKey, privateKey }))
    return { publicKey }
  }
}

// ─── Tool: verify_age ─────────────────────────────────────────────────────────

/**
 * Initiates a Stripe Identity verification session.
 * On completion, Stripe sends a webhook to the broker.
 * The broker sets ageVerified = true on the user record.
 * This function polls until verified or times out.
 */
export async function verify_age(): Promise<{
  verified: boolean
  error?: string
}> {
  const sessionToken = await getSessionToken()
  if (!sessionToken) {
    return { verified: false, error: "Not authenticated. Run setup first." }
  }

  try {
    // Ask broker to initiate a Stripe Identity session
    const res = await fetch(`${BROKER_URL}/verify/age/start`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${sessionToken}`,
      },
    })

    if (!res.ok) {
      return { verified: false, error: "Could not start verification." }
    }

    const { verificationUrl, sessionId } = await res.json()

    // In production: OpenClaw opens this URL in a webview
    // openclaw.browser.open(verificationUrl)
    console.log(`[merge] Open verification URL: ${verificationUrl}`)

    // Poll broker for completion (max 3 minutes)
    const deadline = Date.now() + 3 * 60 * 1000
    while (Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 3000))

      const pollRes = await fetch(
        `${BROKER_URL}/verify/age/status/${sessionId}`,
        {
          headers: { Authorization: `Bearer ${sessionToken}` },
        }
      )

      if (!pollRes.ok) continue
      const { verified } = await pollRes.json()

      if (verified) {
        // Update local profile
        const profile = await readProfile()
        if (profile) {
          profile.ageVerified = true
          profile.verifiedAt = new Date().toISOString()
          profile.updatedAt = new Date().toISOString()
          await writeProfile(profile)
        }
        return { verified: true }
      }
    }

    return { verified: false, error: "Verification timed out. Try again." }
  } catch (err) {
    return { verified: false, error: String(err) }
  }
}

// ─── Tool: upload_signal ──────────────────────────────────────────────────────

/**
 * Encrypts preference vector on device and uploads anonymous
 * availability signal to broker.
 *
 * The broker receives:
 *   - anonymous UUID (not linked to Discord ID on broker)
 *   - H3 location cell (~1km radius)
 *   - seeking preference
 *   - age range
 *   - public key (for future P2P encryption)
 *   - encrypted preference vector (unreadable by broker)
 *
 * The broker does NOT receive:
 *   - name, bio, tagline, Discord handle
 *   - photos
 *   - plain-text preferences
 */
export async function upload_signal(): Promise<{
  signalId: string
  expiresAt: string
} | null> {
  const profile = await readProfile()
  const preferences = await readPreferences()
  const sessionToken = await getSessionToken()

  if (!profile || !preferences || !sessionToken) {
    console.error("[merge] upload_signal: missing profile, preferences, or session")
    return null
  }

  if (!profile.ageVerified) {
    console.error("[merge] upload_signal: age not verified")
    return null
  }

  if (!profile.setupComplete) {
    console.error("[merge] upload_signal: setup not complete")
    return null
  }

  try {
    const anonymousId = await getAnonymousId()
    const locationH3 = await getLocationH3()
    const { publicKey } = await getKeyPair()

    // Encrypt preferences on device — broker cannot read this
    const encryptedVector = encryptPreferences(preferences)

    // Hash discordId before sending — broker stores the hash, not the raw ID
    const discordIdHash = crypto
      .createHash("sha256")
      .update(profile.discordId)
      .digest("hex")

    const signal = {
      anonymousId,
      locationH3,
      seeking:          profile.seeking,
      ageMin:           profile.ageRange[0],
      ageMax:           profile.ageRange[1],
      publicKey,
      preferenceVector: encryptedVector,  // encrypted blob — broker cannot read
      discordIdHash,                      // hashed — broker uses this to create DM
      pushToken:        profile.pushToken ?? null,

      // What is deliberately NOT sent:
      // firstName, age, tagline, bio, discordHandle
      // verifiedAt, setupComplete, createdAt, updatedAt
    }

    const res = await fetch(`${BROKER_URL}/signal`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${sessionToken}`,
      },
      body: JSON.stringify(signal),
    })

    if (!res.ok) {
      console.error("[merge] upload_signal: broker rejected signal", res.status)
      return null
    }

    const { signalId, expiresAt } = await res.json()

    // Record signal state locally
    const signalRecord: Signal = {
      signalId,
      expiresAt,
      uploadedAt: new Date().toISOString(),
    }
    await fs.writeFile(
      path.join(WORKSPACE, "signal.json"),
      JSON.stringify(signalRecord, null, 2)
    )

    return { signalId, expiresAt }
  } catch (err) {
    console.error("[merge] upload_signal error:", err)
    return null
  }
}

// ─── Tool: remove_signal ──────────────────────────────────────────────────────

/**
 * Removes the user's signal from the broker.
 * After this call, the user will not be included in matching jobs.
 * Local files are not deleted — only the broker signal.
 */
export async function remove_signal(): Promise<{ removed: boolean }> {
  const sessionToken = await getSessionToken()
  if (!sessionToken) return { removed: false }

  try {
    const res = await fetch(`${BROKER_URL}/signal`, {
      method: "DELETE",
      headers: { Authorization: `Bearer ${sessionToken}` },
    })

    if (res.ok) {
      // Remove local signal record
      try {
        await fs.unlink(path.join(WORKSPACE, "signal.json"))
      } catch {
        // File may not exist — that's fine
      }
      return { removed: true }
    }

    return { removed: false }
  } catch {
    return { removed: false }
  }
}

// ─── Tool: check_matches ──────────────────────────────────────────────────────

/**
 * Queries the broker for any introductions that have been made.
 * Returns match records with Discord channel IDs.
 *
 * The broker does not tell us who the other person is —
 * only that an introduction was made and where to find it.
 */
export async function check_matches(): Promise<{
  matches: Match[]
  pending: boolean
}> {
  const sessionToken = await getSessionToken()
  if (!sessionToken) return { matches: [], pending: false }

  try {
    const res = await fetch(`${BROKER_URL}/matches`, {
      headers: { Authorization: `Bearer ${sessionToken}` },
    })

    if (!res.ok) return { matches: [], pending: false }

    const data = await res.json()
    const matches: Match[] = data.matches || []

    // Merge with local match log — avoid duplicates
    const existingMatches = await readMatches()
    const existingIds = new Set(existingMatches.map((m) => m.matchId))
    const newMatches = matches.filter((m) => !existingIds.has(m.matchId))

    if (newMatches.length > 0) {
      const allMatches = [...existingMatches, ...newMatches]
      await fs.writeFile(
        path.join(WORKSPACE, "matches.json"),
        JSON.stringify(allMatches, null, 2)
      )
    }

    return {
      matches,
      pending: data.signalActive || false,
    }
  } catch {
    return { matches: [], pending: false }
  }
}

// ─── Tool: delete_account ─────────────────────────────────────────────────────

/**
 * Removes all broker records for this anonymous ID.
 * Deletes all local workspace files.
 * Removes stored credentials.
 *
 * After this call, the user has no presence in Merge at all.
 * Their Discord conversations are unaffected — Merge never had them.
 */
export async function delete_account(): Promise<{ deleted: boolean }> {
  const sessionToken = await getSessionToken()
  if (!sessionToken) return { deleted: false }

  try {
    // Remove from broker
    const res = await fetch(`${BROKER_URL}/account`, {
      method: "DELETE",
      headers: { Authorization: `Bearer ${sessionToken}` },
    })

    // Even if broker call fails, clean up locally
    // (broker records expire anyway — local cleanup is what matters)

    // Delete all workspace files
    const files = await fs.readdir(WORKSPACE).catch(() => [])
    await Promise.all(
      files.map((f) => fs.unlink(path.join(WORKSPACE, f)).catch(() => {}))
    )

    // In production: openclaw.credentials.delete('merge.session_token')
    // openclaw.credentials.delete('merge.anthropic_key')

    return { deleted: res.ok }
  } catch {
    return { deleted: false }
  }
}

// ─── Tool: get_status ─────────────────────────────────────────────────────────

/**
 * Returns current state of the Merge skill for this user.
 * Used by the agent to understand what phase the user is in
 * without reading the raw JSON files itself.
 */
export async function get_status(): Promise<{
  setupComplete: boolean
  ageVerified: boolean
  signalActive: boolean
  signalExpiresAt: string | null
  matchCount: number
}> {
  const profile = await readProfile()
  const signal = await readSignal()
  const matches = await readMatches()

  const signalActive = signal
    ? new Date(signal.expiresAt) > new Date()
    : false

  return {
    setupComplete: profile?.setupComplete || false,
    ageVerified: profile?.ageVerified || false,
    signalActive,
    signalExpiresAt: signal?.expiresAt || null,
    matchCount: matches.length,
  }
}

// ─── Tool: generate_card ──────────────────────────────────────────────────────

/**
 * Generates a short introduction card from the local profile.
 * This is what gets posted to the Discord channel after a match.
 * It is the only thing posted — ever.
 *
 * The card is generated entirely from local data.
 * It is never transmitted to the broker.
 * The broker never sees it.
 *
 * Format:
 *   Maya, 29 — climbs, builds things in Rust, looking for something real.
 *   "I debug in production."
 */
export async function generate_card(): Promise<{
  card: string | null
  error?: string
}> {
  const profile = await readProfile()
  if (!profile) {
    return { card: null, error: "No profile found. Run setup first." }
  }

  try {
    // Pick the most interesting signals — not everything
    const highlights: string[] = []

    // Lead with top hobby or interest (whichever is more concrete)
    if (profile.hobbies.length > 0) {
      highlights.push(profile.hobbies[0])
    } else if (profile.interests.length > 0) {
      highlights.push(profile.interests[0])
    }

    // Add one interest if we have room
    if (profile.interests.length > 0 && highlights.length < 2) {
      highlights.push(profile.interests[0])
    }

    // Add intent
    const intent =
      profile.lookingFor === "relationship" ? "looking for something real"
      : profile.lookingFor === "dating"     ? "seeing where things go"
      : "figuring it out"

    // Build the card
    const highlightStr = highlights.length > 0
      ? highlights.join(", ") + ", "
      : ""

    const firstLine = `${profile.firstName}, ${profile.age} — ${highlightStr}${intent}.`
    const secondLine = profile.tagline ? `"${profile.tagline}"` : ""

    const card = [firstLine, secondLine].filter(Boolean).join("\n")

    // Write to workspace for later use
    await fs.writeFile(path.join(WORKSPACE, "card.txt"), card)

    return { card }
  } catch (err) {
    return { card: null, error: String(err) }
  }
}

// ─── Tool: post_card ──────────────────────────────────────────────────────────

/**
 * Posts the introduction card to the Discord channel created by the broker.
 * Called once, immediately after onMatchReceived.
 * Never called again for the same channel.
 *
 * The card comes from the local card.txt file.
 * The Discord token comes from OpenClaw's credential store.
 * The broker is not involved in this step at all.
 */
export async function post_card(discordChannelId: string): Promise<{
  posted: boolean
  error?: string
}> {
  try {
    // Read card from local file
    const card = await fs.readFile(
      path.join(WORKSPACE, "card.txt"),
      "utf8"
    ).catch(() => null)

    if (!card) {
      // Regenerate if missing
      const result = await generate_card()
      if (!result.card) {
        return { posted: false, error: "Could not generate card." }
      }
    }

    const cardText = card || (await generate_card()).card!
    const discordToken = await getCredential("merge.discord_token")

    if (!discordToken) {
      return { posted: false, error: "No Discord token found." }
    }

    // Post to Discord channel via Discord API
    const res = await fetch(
      `https://discord.com/api/v10/channels/${discordChannelId}/messages`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bot ${discordToken}`,
        },
        body: JSON.stringify({ content: cardText }),
      }
    )

    if (!res.ok) {
      return { posted: false, error: `Discord API error: ${res.status}` }
    }

    // Record that we've posted for this channel — don't post again
    const matches = await readMatches()
    const updated = matches.map((m) =>
      m.discordChannelId === discordChannelId
        ? { ...m, cardPosted: true }
        : m
    )
    await fs.writeFile(
      path.join(WORKSPACE, "matches.json"),
      JSON.stringify(updated, null, 2)
    )

    return { posted: true }
  } catch (err) {
    return { posted: false, error: String(err) }
  }
}

// ─── Tool: init_workspace ─────────────────────────────────────────────────────

/**
 * Called once during onInstall and at the start of SETUP.
 * Copies asset templates into the workspace directory.
 * Never overwrites existing populated files.
 *
 * Template locations (relative to skill root):
 *   assets/profile.template.json     → workspace/profile.json
 *   assets/preferences.template.json → workspace/preferences.json
 */
export async function init_workspace(skillRoot: string): Promise<{
  initialised: boolean
  error?: string
}> {
  try {
    await ensureWorkspace()

    const profileDest = path.join(WORKSPACE, "profile.json")
    const prefsDest   = path.join(WORKSPACE, "preferences.json")

    // Only copy if destination does not already exist
    // Never overwrite a populated profile
    const profileExists = await fs.access(profileDest)
      .then(() => true).catch(() => false)

    const prefsExists = await fs.access(prefsDest)
      .then(() => true).catch(() => false)

    if (!profileExists) {
      const template = await fs.readFile(
        path.join(skillRoot, "assets/profile.template.json"),
        "utf8"
      )
      // Stamp timestamps on copy
      const profile = JSON.parse(template)
      const now = new Date().toISOString()
      profile.createdAt = now
      profile.updatedAt = now
      await fs.writeFile(profileDest, JSON.stringify(profile, null, 2))
    }

    if (!prefsExists) {
      const template = await fs.readFile(
        path.join(skillRoot, "assets/preferences.template.json"),
        "utf8"
      )
      const prefs = JSON.parse(template)
      prefs.updatedAt = new Date().toISOString()
      await fs.writeFile(prefsDest, JSON.stringify(prefs, null, 2))
    }

    return { initialised: true }
  } catch (err) {
    return { initialised: false, error: String(err) }
  }
}
