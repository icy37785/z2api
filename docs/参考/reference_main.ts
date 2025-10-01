// Deno OpenAI-Compatible API Proxy for Z.ai GLM-4.5

// Make this file a module to support top-level await
export {};

// Config variables from environment
const UPSTREAM_URL = Deno.env.get("UPSTREAM_URL") || "https://chat.z.ai/api/chat/completions";
const DEFAULT_KEY = Deno.env.get("DEFAULT_KEY") || "sk-your-key";
const ZAI_TOKEN = Deno.env.get("ZAI_TOKEN") || "";
const MODEL_NAME = Deno.env.get("MODEL_NAME") || "GLM-4.5";
const PORT = parseInt(Deno.env.get("PORT") || "9090");
const DEBUG_MODE = Deno.env.get("DEBUG_MODE") === "true" || true;
const DEFAULT_STREAM = Deno.env.get("DEFAULT_STREAM") !== "false";
const DASHBOARD_ENABLED = Deno.env.get("DASHBOARD_ENABLED") !== "false";
const ENABLE_THINKING = Deno.env.get("ENABLE_THINKING") === "true";

// Browser headers for upstream requests
const X_FE_VERSION = "prod-fe-1.0.70";

// Browser fingerprint generator
function generateBrowserHeaders(chatID: string, authToken: string) {
  const chromeVersion = Math.floor(Math.random() * 3) + 128; // 128-130
  const edgeVersion = chromeVersion;

  const userAgents = [
    `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/${chromeVersion}.0.0.0 Safari/537.36 Edg/${edgeVersion}.0.0.0`,
    `Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/${chromeVersion}.0.0.0 Safari/537.36`,
    `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/${chromeVersion}.0.0.0 Safari/537.36`,
  ];

  const platforms = ['"Windows"', '"macOS"', '"Linux"'];
  const randomUA = userAgents[Math.floor(Math.random() * userAgents.length)];
  const randomPlatform = platforms[Math.floor(Math.random() * platforms.length)];

  return {
    "Content-Type": "application/json",
    "Accept": "*/*",
    "User-Agent": randomUA,
    "Authorization": `Bearer ${authToken}`,
    "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6",
    "Accept-Encoding": "gzip, deflate, br, zstd",
    "sec-ch-ua": `"Chromium";v="${chromeVersion}", "Not(A:Brand";v="24", "Microsoft Edge";v="${edgeVersion}"`,
    "sec-ch-ua-mobile": "?0",
    "sec-ch-ua-platform": randomPlatform,
    "sec-fetch-dest": "empty",
    "sec-fetch-mode": "cors",
    "sec-fetch-site": "same-origin",
    "X-FE-Version": X_FE_VERSION,
    "Origin": ORIGIN_BASE,
    "Referer": `${ORIGIN_BASE}/c/${chatID}`,
    "Priority": "u=1, i",
  };
}

const BROWSER_UA =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36 Edg/139.0.0.0";
const SEC_CH_UA = '"Not;A=Brand";v="99", "Microsoft Edge";v="139", "Chromium";v="139"';
const SEC_CH_UA_MOB = "?0";
const SEC_CH_UA_PLAT = '"Windows"';
const ORIGIN_BASE = "https://chat.z.ai";

// Anonymous token enabled
const ANON_TOKEN_ENABLED = true;

// Thinking tags mode
const THINK_TAGS_MODE = "strip"; // strip | think | raw

// Request statistics
interface RequestStats {
  totalRequests: number;
  successfulRequests: number;
  failedRequests: number;
  lastRequestTime: Date;
  averageResponseTime: number; // in milliseconds
  homePageViews: number; // homepage visits
  apiCallsCount: number; // /v1/chat/completions calls
  modelsCallsCount: number; // /v1/models calls
  streamingRequests: number; // streaming mode requests
  nonStreamingRequests: number; // non-streaming mode requests
  totalTokensUsed: number; // total tokens (approximate)
  startTime: Date; // server start time
  fastestResponse: number; // fastest response time in ms
  slowestResponse: number; // slowest response time in ms
  modelUsage: Map<string, number>; // model name -> count
}

// Hourly and daily stats for persistent storage
interface HourlyStats {
  hour: string; // Format: YYYY-MM-DD-HH
  requests: number;
  success: number;
  failed: number;
  avgResponseTime: number;
  tokens: number;
  models?: Record<string, number>; // model usage
  streamingCount?: number; // streaming requests count
  nonStreamingCount?: number; // non-streaming requests count
  totalMessages?: number; // total messages sent
  uniqueIPs?: Set<string>; // unique client IPs (for this hour)
  errorTypes?: Record<string, number>; // error status codes
}

interface DailyStats {
  date: string; // Format: YYYY-MM-DD
  requests: number;
  success: number;
  failed: number;
  avgResponseTime: number;
  tokens: number;
  peakHour: string;
  models?: Record<string, number>; // model usage
  streamingCount?: number; // streaming requests count
  nonStreamingCount?: number; // non-streaming requests count
  totalMessages?: number; // total messages sent
  uniqueIPsCount?: number; // unique client IPs count for the day
  errorTypes?: Record<string, number>; // error status codes
  fastestResponse?: number; // fastest response of the day
  slowestResponse?: number; // slowest response of the day
}

interface LiveRequest {
  id: string;
  timestamp: Date;
  method: string;
  path: string;
  status: number;
  duration: number; // in milliseconds
  userAgent: string;
  model?: string; // model name if applicable
}

// Global stats
const stats: RequestStats = {
  totalRequests: 0,
  successfulRequests: 0,
  failedRequests: 0,
  lastRequestTime: new Date(),
  averageResponseTime: 0,
  homePageViews: 0,
  apiCallsCount: 0,
  modelsCallsCount: 0,
  streamingRequests: 0,
  nonStreamingRequests: 0,
  totalTokensUsed: 0,
  startTime: new Date(),
  fastestResponse: Infinity,
  slowestResponse: 0,
  modelUsage: new Map<string, number>(),
};

const liveRequests: LiveRequest[] = [];

// Initialize Deno KV database
let kv: Deno.Kv;

// Initialize database connection
async function initDB() {
  try {
    kv = await Deno.openKv();
    debugLog("Deno KV database initialized");
  } catch (error) {
    console.error("Failed to initialize Deno KV:", error);
  }
}

// Get current hour key (format: YYYY-MM-DD-HH)
function getHourKey(): string {
  const now = new Date();
  return `${now.getUTCFullYear()}-${String(now.getUTCMonth() + 1).padStart(2, "0")}-${String(now.getUTCDate()).padStart(2, "0")}-${String(now.getUTCHours()).padStart(2, "0")}`;
}

// Get current date key (format: YYYY-MM-DD)
function getDateKey(): string {
  const now = new Date();
  return `${now.getUTCFullYear()}-${String(now.getUTCMonth() + 1).padStart(2, "0")}-${String(now.getUTCDate()).padStart(2, "0")}`;
}

// Save hourly stats to KV
async function saveHourlyStats(
  duration: number,
  status: number,
  tokens: number,
  model?: string,
  isStreaming?: boolean,
  messageCount?: number,
  clientIP?: string,
) {
  if (!kv) return;

  const hourKey = getHourKey();
  const key = ["stats", "hourly", hourKey];

  try {
    const existing = await kv.get<HourlyStats>(key);
    const current = existing.value || {
      hour: hourKey,
      requests: 0,
      success: 0,
      failed: 0,
      avgResponseTime: 0,
      tokens: 0,
      models: {},
      streamingCount: 0,
      nonStreamingCount: 0,
      totalMessages: 0,
      uniqueIPs: new Set<string>(),
      errorTypes: {},
    };

    current.requests++;
    if (status >= 200 && status < 300) {
      current.success++;
    } else {
      current.failed++;
      // Track error types
      if (!current.errorTypes) current.errorTypes = {};
      current.errorTypes[status] = (current.errorTypes[status] || 0) + 1;
    }

    // Update average response time
    const totalTime = current.avgResponseTime * (current.requests - 1) + duration;
    current.avgResponseTime = totalTime / current.requests;
    current.tokens += tokens;

    // Track model usage
    if (model && current.models) {
      current.models[model] = (current.models[model] || 0) + 1;
    }

    // Track streaming vs non-streaming
    if (isStreaming !== undefined) {
      if (isStreaming) {
        current.streamingCount = (current.streamingCount || 0) + 1;
      } else {
        current.nonStreamingCount = (current.nonStreamingCount || 0) + 1;
      }
    }

    // Track message count
    if (messageCount) {
      current.totalMessages = (current.totalMessages || 0) + messageCount;
    }

    // Track unique IPs
    if (clientIP && clientIP !== "unknown") {
      if (!current.uniqueIPs) current.uniqueIPs = new Set();
      current.uniqueIPs.add(clientIP);
    }

    // Convert Set to Array for storage
    const dataToStore = {
      ...current,
      uniqueIPs: Array.from(current.uniqueIPs || []),
    };

    await kv.set(key, dataToStore, { expireIn: 7 * 24 * 60 * 60 * 1000 }); // Expire after 7 days
  } catch (error) {
    debugLog("Error saving hourly stats:", error);
  }
}

// Save daily stats to KV
async function saveDailyStats() {
  if (!kv) return;

  const dateKey = getDateKey();
  const key = ["stats", "daily", dateKey];

  try {
    // Aggregate all hourly stats for today
    const prefix = ["stats", "hourly"];
    const entries = kv.list<HourlyStats>({ prefix });

    let totalRequests = 0;
    let totalSuccess = 0;
    let totalFailed = 0;
    let totalResponseTime = 0;
    let totalTokens = 0;
    let peakHour = "";
    let peakRequests = 0;
    const modelUsage: Record<string, number> = {};

    for await (const entry of entries) {
      if (entry.value.hour.startsWith(dateKey)) {
        totalRequests += entry.value.requests;
        totalSuccess += entry.value.success;
        totalFailed += entry.value.failed;
        totalResponseTime += entry.value.avgResponseTime * entry.value.requests;
        totalTokens += entry.value.tokens;

        if (entry.value.requests > peakRequests) {
          peakRequests = entry.value.requests;
          peakHour = entry.value.hour;
        }

        // Aggregate model usage
        if (entry.value.models) {
          for (const [model, count] of Object.entries(entry.value.models)) {
            modelUsage[model] = (modelUsage[model] || 0) + count;
          }
        }
      }
    }

    const dailyStat: DailyStats = {
      date: dateKey,
      requests: totalRequests,
      success: totalSuccess,
      failed: totalFailed,
      avgResponseTime: totalRequests > 0 ? totalResponseTime / totalRequests : 0,
      tokens: totalTokens,
      peakHour,
      models: modelUsage,
    };

    await kv.set(key, dailyStat, { expireIn: 30 * 24 * 60 * 60 * 1000 }); // Expire after 30 days
  } catch (error) {
    debugLog("Error saving daily stats:", error);
  }
}

// Get hourly stats for last N hours
async function getHourlyStats(hours = 24): Promise<HourlyStats[]> {
  if (!kv) return [];

  const result: HourlyStats[] = [];
  const prefix = ["stats", "hourly"];

  try {
    const entries = kv.list<HourlyStats>({ prefix, reverse: true, limit: hours });
    for await (const entry of entries) {
      result.push(entry.value);
    }
  } catch (error) {
    debugLog("Error getting hourly stats:", error);
  }

  return result.reverse();
}

// Get daily stats for last N days
async function getDailyStats(days = 30): Promise<DailyStats[]> {
  if (!kv) return [];

  const result: DailyStats[] = [];
  const prefix = ["stats", "daily"];

  try {
    const entries = kv.list<DailyStats>({ prefix, reverse: true, limit: days });
    for await (const entry of entries) {
      result.push(entry.value);
    }
  } catch (error) {
    debugLog("Error getting daily stats:", error);
  }

  return result.reverse();
}

// Cleanup old data (called periodically)
async function cleanupOldData() {
  if (!kv) return;

  try {
    // Delete hourly data older than 7 days
    const sevenDaysAgo = new Date(Date.now() - 7 * 24 * 60 * 60 * 1000);
    const cutoffHour = `${sevenDaysAgo.getUTCFullYear()}-${String(sevenDaysAgo.getUTCMonth() + 1).padStart(2, "0")}-${String(sevenDaysAgo.getUTCDate()).padStart(2, "0")}-${String(sevenDaysAgo.getUTCHours()).padStart(2, "0")}`;

    const hourlyPrefix = ["stats", "hourly"];
    const hourlyEntries = kv.list({ prefix: hourlyPrefix });

    for await (const entry of hourlyEntries) {
      const hour = entry.key[2] as string;
      if (hour < cutoffHour) {
        await kv.delete(entry.key);
        debugLog("Deleted old hourly data:", hour);
      }
    }

    // Delete daily data older than 30 days
    const thirtyDaysAgo = new Date(Date.now() - 30 * 24 * 60 * 60 * 1000);
    const cutoffDate = `${thirtyDaysAgo.getUTCFullYear()}-${String(thirtyDaysAgo.getUTCMonth() + 1).padStart(2, "0")}-${String(thirtyDaysAgo.getUTCDate()).padStart(2, "0")}`;

    const dailyPrefix = ["stats", "daily"];
    const dailyEntries = kv.list({ prefix: dailyPrefix });

    for await (const entry of dailyEntries) {
      const date = entry.key[2] as string;
      if (date < cutoffDate) {
        await kv.delete(entry.key);
        debugLog("Deleted old daily data:", date);
      }
    }
  } catch (error) {
    debugLog("Error cleaning up old data:", error);
  }
}

// OpenAI request/response types
interface Message {
  role: string;
  content: string;
}

interface OpenAIRequest {
  model: string;
  messages: Message[];
  stream?: boolean;
  temperature?: number;
  max_tokens?: number;
  enable_thinking?: boolean;
}

interface UpstreamRequest {
  stream: boolean;
  model: string;
  messages: Message[];
  params: Record<string, unknown>;
  features: Record<string, unknown>;
  background_tasks?: Record<string, boolean>;
  chat_id?: string;
  id?: string;
  mcp_servers?: string[];
  model_item?: {
    id: string;
    name: string;
    owned_by: string;
  };
  tool_servers?: string[];
  variables?: Record<string, string>;
}

interface OpenAIResponse {
  id: string;
  object: string;
  created: number;
  model: string;
  choices: Choice[];
  usage?: Usage;
}

interface Choice {
  index: number;
  message?: Message;
  delta?: Delta;
  finish_reason?: string;
}

interface Delta {
  role?: string;
  content?: string;
}

interface Usage {
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
}

interface UpstreamData {
  type?: string;
  data: {
    delta_content: string;
    phase: string;
    done: boolean;
    usage?: Usage;
    error?: UpstreamError;
    data?: {
      error?: UpstreamError;
    };
  };
  error?: UpstreamError;
}

interface UpstreamError {
  detail: string;
  code: number;
}

interface Model {
  id: string;
  object: string;
  created: number;
  owned_by: string;
}

// Debug logger
function debugLog(...args: unknown[]) {
  if (DEBUG_MODE) {
    console.log("[DEBUG]", ...args);
  }
}

// Record request stats
async function recordRequestStats(
  startTime: number,
  path: string,
  status: number,
  tokens = 0,
  model?: string,
  isStreaming?: boolean,
  messageCount?: number,
  clientIP?: string,
) {
  const duration = Date.now() - startTime;

  stats.totalRequests++;
  stats.lastRequestTime = new Date();

  if (status >= 200 && status < 300) {
    stats.successfulRequests++;
  } else {
    stats.failedRequests++;
  }

  // Track endpoint-specific stats
  if (path === "/v1/chat/completions") {
    stats.apiCallsCount++;
  } else if (path === "/v1/models") {
    stats.modelsCallsCount++;
  }

  // Track tokens
  if (tokens > 0) {
    stats.totalTokensUsed += tokens;
  }

  // Track model usage
  if (model) {
    const count = stats.modelUsage.get(model) || 0;
    stats.modelUsage.set(model, count + 1);
  }

  // Update response time stats
  if (duration < stats.fastestResponse) {
    stats.fastestResponse = duration;
  }
  if (duration > stats.slowestResponse) {
    stats.slowestResponse = duration;
  }

  // Update average response time
  if (stats.totalRequests > 0) {
    const totalDuration = stats.averageResponseTime * (stats.totalRequests - 1) + duration;
    stats.averageResponseTime = totalDuration / stats.totalRequests;
  } else {
    stats.averageResponseTime = duration;
  }

  // Save to KV database (async, don't await to avoid blocking)
  saveHourlyStats(duration, status, tokens, model, isStreaming, messageCount, clientIP).catch((err) =>
    debugLog("Error saving hourly stats:", err)
  );
}

// Add live request
function addLiveRequest(
  method: string,
  path: string,
  status: number,
  duration: number,
  _clientIP: string,
  userAgent: string,
  model?: string,
) {
  const request: LiveRequest = {
    id: `${Date.now()}${Math.random()}`,
    timestamp: new Date(),
    method,
    path,
    status,
    duration,
    userAgent,
    model,
  };

  liveRequests.push(request);

  // Keep only last 100 requests
  if (liveRequests.length > 100) {
    liveRequests.shift();
  }
}

// Get client IP
function getClientIP(req: Request): string {
  const xff = req.headers.get("X-Forwarded-For");
  if (xff) {
    const ips = xff.split(",");
    if (ips.length > 0) {
      return ips[0].trim();
    }
  }

  const xri = req.headers.get("X-Real-IP");
  if (xri) {
    return xri;
  }

  return "unknown";
}

// Get anonymous token
async function getAnonymousToken(): Promise<string> {
  try {
    const response = await fetch(`${ORIGIN_BASE}/api/v1/auths/`, {
      method: "GET",
      headers: {
        "User-Agent": BROWSER_UA,
        "Accept": "*/*",
        "Accept-Language": "zh-CN,zh;q=0.9",
        "X-FE-Version": X_FE_VERSION,
        "sec-ch-ua": SEC_CH_UA,
        "sec-ch-ua-mobile": SEC_CH_UA_MOB,
        "sec-ch-ua-platform": SEC_CH_UA_PLAT,
        "Origin": ORIGIN_BASE,
        "Referer": `${ORIGIN_BASE}/`,
      },
    });

    if (!response.ok) {
      throw new Error(`Failed to get anon token: ${response.status}`);
    }

    const data = await response.json();
    if (!data.token) {
      throw new Error("Empty token in response");
    }

    return data.token;
  } catch (error) {
    debugLog("Anonymous token error:", error);
    throw error;
  }
}

// Transform thinking content
function transformThinking(s: string): string {
  // Remove <summary>…</summary>
  s = s.replace(/<summary>.*?<\/summary>/gs, "");
  // Clean up custom tags
  s = s.replace(/<\/thinking>/g, "");
  s = s.replace(/<Full>/g, "");
  s = s.replace(/<\/Full>/g, "");
  s = s.trim();

  switch (THINK_TAGS_MODE) {
    case "think":
      s = s.replace(/<details[^>]*>/g, "<think>");
      s = s.replace(/<\/details>/g, "</think>");
      break;
    case "strip":
      s = s.replace(/<details[^>]*>/g, "");
      s = s.replace(/<\/details>/g, "");
      break;
  }

  // Remove "> " prefix
  s = s.replace(/^> /, "");
  s = s.replace(/\n> /g, "\n");

  return s.trim();
}

// Call upstream API
async function callUpstream(
  upstreamReq: UpstreamRequest,
  chatID: string,
  authToken: string,
): Promise<Response> {
  debugLog("Calling upstream:", UPSTREAM_URL);

  // Generate dynamic browser headers for better fingerprinting
  const headers = generateBrowserHeaders(chatID, authToken);

  const response = await fetch(UPSTREAM_URL, {
    method: "POST",
    headers: headers,
    body: JSON.stringify(upstreamReq),
  });

  debugLog("Upstream response status:", response.status);
  return response;
}

// Handle stream response
async function handleStreamResponse(
  upstreamReq: UpstreamRequest,
  chatID: string,
  authToken: string,
  startTime: number,
  path: string,
  clientIP: string,
  userAgent: string,
  model: string,
  messageCount: number,
): Promise<Response> {
  debugLog("Handling stream response, chat_id:", chatID);

  const upstreamResp = await callUpstream(upstreamReq, chatID, authToken);

  if (!upstreamResp.ok) {
    debugLog("Upstream error status:", upstreamResp.status);
    const duration = Date.now() - startTime;
    recordRequestStats(startTime, path, 502, 0, model, true, messageCount, clientIP);
    addLiveRequest("POST", path, 502, duration, clientIP, userAgent, model);
    return new Response("Upstream error", { status: 502 });
  }

  const encoder = new TextEncoder();
  let isFirstChunk = true;

  const stream = new ReadableStream({
    async start(controller) {
      let streamClosed = false;

      const safeEnqueue = (data: Uint8Array) => {
        if (!streamClosed) {
          try {
            controller.enqueue(data);
          } catch (e) {
            debugLog("Enqueue error:", e);
            streamClosed = true;
          }
        }
      };

      const safeClose = () => {
        if (!streamClosed) {
          try {
            controller.close();
            streamClosed = true;
          } catch (e) {
            debugLog("Close error:", e);
            streamClosed = true;
          }
        }
      };

      try {
        const reader = upstreamResp.body?.getReader();
        if (!reader) {
          safeClose();
          return;
        }

        const decoder = new TextDecoder();
        let buffer = "";

        // Send first chunk with role
        if (isFirstChunk) {
          const firstChunk: OpenAIResponse = {
            id: `chatcmpl-${Date.now()}`,
            object: "chat.completion.chunk",
            created: Math.floor(Date.now() / 1000),
            model: MODEL_NAME,
            choices: [{
              index: 0,
              delta: { role: "assistant" },
            }],
          };
          safeEnqueue(encoder.encode(`data: ${JSON.stringify(firstChunk)}\n\n`));
          isFirstChunk = false;
        }

        while (true) {
          const { done, value } = await reader.read();
          if (done) break;

          buffer += decoder.decode(value, { stream: true });
          const lines = buffer.split("\n");
          buffer = lines.pop() || "";

          for (const line of lines) {
            if (!line.startsWith("data: ")) continue;

            const dataStr = line.substring(6);
            if (!dataStr) continue;

            try {
              const upstreamData: UpstreamData = JSON.parse(dataStr);

              // Check for errors
              if (
                upstreamData.error || upstreamData.data?.error ||
                upstreamData.data?.data?.error
              ) {
                debugLog("Upstream error detected:", JSON.stringify(upstreamData));
                const endChunk: OpenAIResponse = {
                  id: `chatcmpl-${Date.now()}`,
                  object: "chat.completion.chunk",
                  created: Math.floor(Date.now() / 1000),
                  model: MODEL_NAME,
                  choices: [{
                    index: 0,
                    delta: {},
                    finish_reason: "stop",
                  }],
                };
                safeEnqueue(encoder.encode(`data: ${JSON.stringify(endChunk)}\n\n`));
                safeEnqueue(encoder.encode("data: [DONE]\n\n"));
                safeClose();
                // Record stats before returning
                const duration = Date.now() - startTime;
                recordRequestStats(startTime, path, 200, 0, model, true, messageCount, clientIP);
                addLiveRequest("POST", path, 200, duration, clientIP, userAgent, model);
                return;
              }

              // Process content
              if (upstreamData.data.delta_content) {
                let out = upstreamData.data.delta_content;
                if (upstreamData.data.phase === "thinking") {
                  out = transformThinking(out);
                }

                if (out) {
                  const chunk: OpenAIResponse = {
                    id: `chatcmpl-${Date.now()}`,
                    object: "chat.completion.chunk",
                    created: Math.floor(Date.now() / 1000),
                    model: MODEL_NAME,
                    choices: [{
                      index: 0,
                      delta: { content: out },
                    }],
                  };
                  safeEnqueue(encoder.encode(`data: ${JSON.stringify(chunk)}\n\n`));
                }
              }

              // Check if done
              if (upstreamData.data.done || upstreamData.data.phase === "done") {
                debugLog("Stream done");
                const endChunk: OpenAIResponse = {
                  id: `chatcmpl-${Date.now()}`,
                  object: "chat.completion.chunk",
                  created: Math.floor(Date.now() / 1000),
                  model: MODEL_NAME,
                  choices: [{
                    index: 0,
                    delta: {},
                    finish_reason: "stop",
                  }],
                };
                safeEnqueue(encoder.encode(`data: ${JSON.stringify(endChunk)}\n\n`));
                safeEnqueue(encoder.encode("data: [DONE]\n\n"));
                safeClose();
                // Record stats before returning
                const duration = Date.now() - startTime;
                recordRequestStats(startTime, path, 200, 0, model, true, messageCount, clientIP);
                addLiveRequest("POST", path, 200, duration, clientIP, userAgent, model);
                return;
              }
            } catch (e) {
              debugLog("Parse error:", e);
            }
          }
        }

        safeClose();

        // Record stats
        const duration = Date.now() - startTime;
        recordRequestStats(startTime, path, 200, 0, model, true, messageCount, clientIP);
        addLiveRequest("POST", path, 200, duration, clientIP, userAgent, model);
      } catch (error) {
        debugLog("Stream error:", error);
        if (!streamClosed) {
          try {
            controller.error(error);
            streamClosed = true;
          } catch (e) {
            debugLog("Error calling controller.error:", e);
          }
        }
      }
    },
  });

  return new Response(stream, {
    status: 200,
    headers: {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      "Connection": "keep-alive",
      "Access-Control-Allow-Origin": "*",
    },
  });
}

// Handle non-stream response
async function handleNonStreamResponse(
  upstreamReq: UpstreamRequest,
  chatID: string,
  authToken: string,
  startTime: number,
  path: string,
  clientIP: string,
  userAgent: string,
  model: string,
  messageCount: number,
): Promise<Response> {
  debugLog("Handling non-stream response, chat_id:", chatID);

  const upstreamResp = await callUpstream(upstreamReq, chatID, authToken);

  if (!upstreamResp.ok) {
    debugLog("Upstream error status:", upstreamResp.status);
    const duration = Date.now() - startTime;
    recordRequestStats(startTime, path, 502, 0, model, false, messageCount, clientIP);
    addLiveRequest("POST", path, 502, duration, clientIP, userAgent, model);
    return new Response("Upstream error", { status: 502 });
  }

  const reader = upstreamResp.body?.getReader();
  if (!reader) {
    return new Response("No response body", { status: 502 });
  }

  const decoder = new TextDecoder();
  let buffer = "";
  let fullContent = "";

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;

    buffer += decoder.decode(value, { stream: true });
    const lines = buffer.split("\n");
    buffer = lines.pop() || "";

    for (const line of lines) {
      if (!line.startsWith("data: ")) continue;

      const dataStr = line.substring(6);
      if (!dataStr || dataStr === "[DONE]") continue; // Skip empty or DONE markers

      try {
        const upstreamData: UpstreamData = JSON.parse(dataStr);

        if (upstreamData.data.delta_content) {
          let out = upstreamData.data.delta_content;
          if (upstreamData.data.phase === "thinking") {
            out = transformThinking(out, enableThinking);
          }
          if (out) {
            fullContent += out;
          }
        }

        if (upstreamData.data.done || upstreamData.data.phase === "done") {
          break;
        }
      } catch (e) {
        debugLog("Parse error:", e);
      }
    }
  }

  const response: OpenAIResponse = {
    id: `chatcmpl-${Date.now()}`,
    object: "chat.completion",
    created: Math.floor(Date.now() / 1000),
    model: MODEL_NAME,
    choices: [{
      index: 0,
      message: {
        role: "assistant",
        content: fullContent,
      },
      finish_reason: "stop",
    }],
    usage: {
      prompt_tokens: 0,
      completion_tokens: 0,
      total_tokens: 0,
    },
  };

  const duration = Date.now() - startTime;
  recordRequestStats(startTime, path, 200, 0, model, false, messageCount, clientIP);
  addLiveRequest("POST", path, 200, duration, clientIP, userAgent, model);

  return new Response(JSON.stringify(response), {
    status: 200,
    headers: {
      "Content-Type": "application/json",
      "Access-Control-Allow-Origin": "*",
    },
  });
}

// CORS headers
function setCORSHeaders(headers: Headers) {
  headers.set("Access-Control-Allow-Origin", "*");
  headers.set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS");
  headers.set("Access-Control-Allow-Headers", "Content-Type, Authorization");
  headers.set("Access-Control-Allow-Credentials", "true");
}

// Handle models endpoint
async function handleModels(req: Request): Promise<Response> {
  const startTime = Date.now();
  const clientIP = getClientIP(req);
  const userAgent = req.headers.get("User-Agent") || "";

  try {
    // Get token (ZAI_TOKEN or anonymous)
    let token = ZAI_TOKEN;
    if (!token) {
      token = await getAnonymousToken();
      if (!token) {
        debugLog("Failed to get anonymous token for models request");
        const duration = Date.now() - startTime;
        recordRequestStats(startTime, "/v1/models", 500, 0, undefined, undefined, undefined, clientIP);
        addLiveRequest("GET", "/v1/models", 500, duration, clientIP, userAgent);
        return new Response(JSON.stringify({ error: "Failed to authenticate" }), {
          status: 500,
          headers: { "Content-Type": "application/json" },
        });
      }
    }

    // Request models from upstream
    const upstreamResponse = await fetch("https://chat.z.ai/api/models", {
      method: "GET",
      headers: {
        "Accept": "application/json",
        "Accept-Language": "zh-CN",
        "Content-Type": "application/json",
        "Authorization": `Bearer ${token}`,
        "User-Agent": BROWSER_UA,
        "Referer": "https://chat.z.ai/",
        "sec-ch-ua": SEC_CH_UA,
        "sec-ch-ua-mobile": SEC_CH_UA_MOB,
        "sec-ch-ua-platform": SEC_CH_UA_PLAT,
        "Sec-Fetch-Dest": "empty",
        "Sec-Fetch-Mode": "cors",
        "Sec-Fetch-Site": "same-origin",
      },
    });

    if (!upstreamResponse.ok) {
      debugLog(`Upstream models request failed: ${upstreamResponse.status}`);
      throw new Error(`Upstream returned ${upstreamResponse.status}`);
    }

    const upstreamData = await upstreamResponse.json();

    // Transform to OpenAI format
    const models = upstreamData.data.map((model: any) => ({
      id: model.name || model.id,
      object: "model",
      created: Math.floor(Date.now() / 1000),
      owned_by: "z.ai",
    }));

    const response = {
      object: "list",
      data: models,
    };

    const headers = new Headers({ "Content-Type": "application/json" });
    setCORSHeaders(headers);

    // Record successful stats
    const duration = Date.now() - startTime;
    recordRequestStats(startTime, "/v1/models", 200, 0, undefined, undefined, undefined, clientIP);
    addLiveRequest("GET", "/v1/models", 200, duration, clientIP, userAgent);

    return new Response(JSON.stringify(response), { status: 200, headers });
  } catch (error) {
    debugLog(`Error fetching models: ${error}`);

    // Fallback to default model
    const response = {
      object: "list",
      data: [{
        id: MODEL_NAME,
        object: "model",
        created: Math.floor(Date.now() / 1000),
        owned_by: "z.ai",
      }],
    };

    const headers = new Headers({ "Content-Type": "application/json" });
    setCORSHeaders(headers);

    // Record error stats (still return 200 with fallback data)
    const duration = Date.now() - startTime;
    recordRequestStats(startTime, "/v1/models", 200, 0, undefined, undefined, undefined, clientIP);
    addLiveRequest("GET", "/v1/models", 200, duration, clientIP, userAgent);

    return new Response(JSON.stringify(response), { status: 200, headers });
  }
}

// Handle chat completions
async function handleChatCompletions(req: Request): Promise<Response> {
  const startTime = Date.now();
  const url = new URL(req.url);
  const path = url.pathname;
  const clientIP = getClientIP(req);
  const userAgent = req.headers.get("User-Agent") || "";

  debugLog("Received chat completions request");

  // Verify API key
  const authHeader = req.headers.get("Authorization");
  if (!authHeader?.startsWith("Bearer ")) {
    debugLog("Missing or invalid Authorization header");
    const duration = Date.now() - startTime;
    recordRequestStats(startTime, path, 401);
    addLiveRequest("POST", path, 401, duration, clientIP, userAgent);
    return new Response("Missing or invalid Authorization header", { status: 401 });
  }

  const apiKey = authHeader.substring(7);
  if (apiKey !== DEFAULT_KEY) {
    debugLog("Invalid API key:", apiKey);
    const duration = Date.now() - startTime;
    recordRequestStats(startTime, path, 401);
    addLiveRequest("POST", path, 401, duration, clientIP, userAgent);
    return new Response("Invalid API key", { status: 401 });
  }

  debugLog("API key verified");

  // Parse request
  let body: OpenAIRequest;
  try {
    body = await req.json();
  } catch (e) {
    debugLog("JSON parse error:", e);
    const duration = Date.now() - startTime;
    recordRequestStats(startTime, path, 400);
    addLiveRequest("POST", path, 400, duration, clientIP, userAgent);
    return new Response("Invalid JSON", { status: 400 });
  }

  // Set default stream if not specified
  if (body.stream === undefined) {
    body.stream = DEFAULT_STREAM;
    debugLog("Using default stream value:", DEFAULT_STREAM);
  }

  // Track streaming vs non-streaming requests
  if (body.stream) {
    stats.streamingRequests++;
  } else {
    stats.nonStreamingRequests++;
  }

  debugLog(
    `Request parsed - model: ${body.model}, stream: ${body.stream}, messages: ${body.messages.length}`,
  );

  // Generate chat IDs
  const chatID = `${Date.now()}-${Math.floor(Math.random() * 100000)}`;
  const msgID = `${Date.now()}`;

  // Determine thinking setting
  const enableThinking = body.enable_thinking !== undefined
    ? body.enable_thinking
    : ENABLE_THINKING;
  debugLog("Enable thinking:", enableThinking);

  // Build upstream request
  const upstreamReq: UpstreamRequest = {
    stream: true,
    chat_id: chatID,
    id: msgID,
    model: "0727-360B-API",
    messages: body.messages,
    params: {},
    features: {
      enable_thinking: enableThinking,
    },
    background_tasks: {
      title_generation: false,
      tags_generation: false,
    },
    mcp_servers: [],
    model_item: {
      id: "0727-360B-API",
      name: "GLM-4.5",
      owned_by: "openai",
    },
    tool_servers: [],
    variables: {
      "{{USER_NAME}}": "User",
      "{{USER_LOCATION}}": "Unknown",
      "{{CURRENT_DATETIME}}": new Date().toISOString().replace("T", " ").substring(0, 19),
    },
  };

  // Get auth token
  let authToken = ZAI_TOKEN;
  if (ANON_TOKEN_ENABLED) {
    try {
      authToken = await getAnonymousToken();
      debugLog("Anonymous token obtained:", authToken.substring(0, 10) + "...");
    } catch (e) {
      debugLog("Failed to get anonymous token, using fixed token:", e);
    }
  }

  // Call upstream
  if (body.stream) {
    return await handleStreamResponse(
      upstreamReq,
      chatID,
      authToken,
      startTime,
      path,
      clientIP,
      userAgent,
      body.model,
      body.messages.length,
    );
  } else {
    return await handleNonStreamResponse(
      upstreamReq,
      chatID,
      authToken,
      startTime,
      path,
      clientIP,
      userAgent,
      body.model,
      body.messages.length,
    );
  }
}

// Dashboard HTML
const dashboardHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Dashboard - ZtoApi</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
</head>
<body class="bg-gray-50">
    <nav class="bg-white shadow-sm border-b">
        <div class="container mx-auto px-4 py-4">
            <div class="flex items-center justify-between">
                <a href="/" class="flex items-center space-x-2 text-purple-600 hover:text-purple-700 transition">
                    <span class="text-2xl">🦕</span>
                    <span class="text-xl font-bold">ZtoApi</span>
                </a>
                <div class="flex space-x-4">
                    <a href="/" class="text-gray-600 hover:text-purple-600 transition">首页</a>
                    <a href="/docs" class="text-gray-600 hover:text-purple-600 transition">文档</a>
                    <a href="/deploy" class="text-gray-600 hover:text-purple-600 transition">部署</a>
                    <a href="/dashboard" class="text-purple-600 font-semibold">Dashboard</a>
                </div>
            </div>
        </div>
    </nav>

    <div class="container mx-auto px-4 py-8 max-w-7xl">
        <div class="text-center mb-8">
            <h1 class="text-4xl font-bold text-gray-900 mb-3">📊 Dashboard</h1>
            <p class="text-gray-600">实时监控 API 请求和性能统计</p>
        </div>

        <!-- Stats Cards -->
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-5 gap-6 mb-8">
            <div class="bg-white rounded-xl shadow-sm border p-6 hover:shadow-md transition">
                <div class="flex items-center justify-between">
                    <div>
                        <p class="text-gray-600 text-sm mb-1">总请求数</p>
                        <p class="text-3xl font-bold text-gray-900" id="total">0</p>
                    </div>
                    <div class="bg-purple-100 p-3 rounded-lg">
                        <span class="text-3xl">📈</span>
                    </div>
                </div>
            </div>

            <div class="bg-white rounded-xl shadow-sm border p-6 hover:shadow-md transition">
                <div class="flex items-center justify-between">
                    <div>
                        <p class="text-gray-600 text-sm mb-1">成功请求</p>
                        <p class="text-3xl font-bold text-green-600" id="success">0</p>
                    </div>
                    <div class="bg-green-100 p-3 rounded-lg">
                        <span class="text-3xl">✅</span>
                    </div>
                </div>
            </div>

            <div class="bg-white rounded-xl shadow-sm border p-6 hover:shadow-md transition">
                <div class="flex items-center justify-between">
                    <div>
                        <p class="text-gray-600 text-sm mb-1">失败请求</p>
                        <p class="text-3xl font-bold text-red-600" id="failed">0</p>
                    </div>
                    <div class="bg-red-100 p-3 rounded-lg">
                        <span class="text-3xl">❌</span>
                    </div>
                </div>
            </div>

            <div class="bg-white rounded-xl shadow-sm border p-6 hover:shadow-md transition">
                <div class="flex items-center justify-between">
                    <div>
                        <p class="text-gray-600 text-sm mb-1">平均响应时间</p>
                        <p class="text-3xl font-bold text-blue-600" id="avgtime">0ms</p>
                    </div>
                    <div class="bg-blue-100 p-3 rounded-lg">
                        <span class="text-3xl">⚡</span>
                    </div>
                </div>
            </div>

            <div class="bg-white rounded-xl shadow-sm border p-6 hover:shadow-md transition">
                <div class="flex items-center justify-between">
                    <div>
                        <p class="text-gray-600 text-sm mb-1">首页访问</p>
                        <p class="text-3xl font-bold text-indigo-600" id="homeviews">0</p>
                    </div>
                    <div class="bg-indigo-100 p-3 rounded-lg">
                        <span class="text-3xl">🏠</span>
                    </div>
                </div>
            </div>
        </div>

        <!-- Detailed Stats Grid -->
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6 mb-8">
            <!-- API Stats -->
            <div class="bg-white rounded-xl shadow-sm border p-6">
                <h3 class="text-lg font-bold text-gray-900 mb-4 flex items-center">
                    <span class="text-2xl mr-2">🎯</span> API 统计
                </h3>
                <div class="space-y-3">
                    <div class="flex justify-between items-center">
                        <span class="text-gray-600 text-sm">Chat Completions</span>
                        <span class="font-bold text-purple-600" id="api-calls">0</span>
                    </div>
                    <div class="flex justify-between items-center">
                        <span class="text-gray-600 text-sm">Models 查询</span>
                        <span class="font-bold text-purple-600" id="models-calls">0</span>
                    </div>
                    <div class="flex justify-between items-center">
                        <span class="text-gray-600 text-sm">流式请求</span>
                        <span class="font-bold text-blue-600" id="streaming">0</span>
                    </div>
                    <div class="flex justify-between items-center">
                        <span class="text-gray-600 text-sm">非流式请求</span>
                        <span class="font-bold text-blue-600" id="non-streaming">0</span>
                    </div>
                </div>
            </div>

            <!-- Performance Stats -->
            <div class="bg-white rounded-xl shadow-sm border p-6">
                <h3 class="text-lg font-bold text-gray-900 mb-4 flex items-center">
                    <span class="text-2xl mr-2">⚡</span> 性能指标
                </h3>
                <div class="space-y-3">
                    <div class="flex justify-between items-center">
                        <span class="text-gray-600 text-sm">平均响应</span>
                        <span class="font-bold text-blue-600" id="avg-time-detail">0ms</span>
                    </div>
                    <div class="flex justify-between items-center">
                        <span class="text-gray-600 text-sm">最快响应</span>
                        <span class="font-bold text-green-600" id="fastest">0ms</span>
                    </div>
                    <div class="flex justify-between items-center">
                        <span class="text-gray-600 text-sm">最慢响应</span>
                        <span class="font-bold text-orange-600" id="slowest">0ms</span>
                    </div>
                    <div class="flex justify-between items-center">
                        <span class="text-gray-600 text-sm">成功率</span>
                        <span class="font-bold text-green-600" id="success-rate">0%</span>
                    </div>
                </div>
            </div>

            <!-- System Info -->
            <div class="bg-white rounded-xl shadow-sm border p-6">
                <h3 class="text-lg font-bold text-gray-900 mb-4 flex items-center">
                    <span class="text-2xl mr-2">📊</span> 系统信息
                </h3>
                <div class="space-y-3">
                    <div class="flex justify-between items-center">
                        <span class="text-gray-600 text-sm">运行时长</span>
                        <span class="font-bold text-indigo-600" id="uptime">0</span>
                    </div>
                    <div class="flex justify-between items-center">
                        <span class="text-gray-600 text-sm">Token 使用</span>
                        <span class="font-bold text-indigo-600" id="tokens">0</span>
                    </div>
                    <div class="flex justify-between items-center">
                        <span class="text-gray-600 text-sm">最后请求</span>
                        <span class="font-bold text-gray-600 text-xs" id="last-request">-</span>
                    </div>
                    <div class="flex justify-between items-center">
                        <span class="text-gray-600 text-sm">首页访问</span>
                        <span class="font-bold text-indigo-600" id="home-visits">0</span>
                    </div>
                </div>
            </div>
        </div>

        <!-- Top Models Card -->
        <div class="bg-white rounded-xl shadow-sm border p-6 mb-8">
            <h3 class="text-lg font-bold text-gray-900 mb-4 flex items-center">
                <span class="text-2xl mr-2">🏆</span> 热门模型 Top 3
            </h3>
            <div id="top-models" class="space-y-3">
                <p class="text-gray-500 text-sm">暂无数据</p>
            </div>
        </div>

        <!-- Chart -->
        <div class="bg-white rounded-xl shadow-sm border p-6 mb-8">
            <div class="flex items-center justify-between mb-4">
                <h2 class="text-xl font-bold text-gray-900">📉 请求趋势分析</h2>
                <div class="flex gap-2">
                    <button id="view-hourly" class="px-4 py-2 bg-purple-600 text-white rounded-lg hover:bg-purple-700 transition text-sm font-semibold">按小时</button>
                    <button id="view-daily" class="px-4 py-2 bg-gray-200 text-gray-700 rounded-lg hover:bg-gray-300 transition text-sm font-semibold">按天</button>
                </div>
            </div>

            <!-- Info banner -->
            <div class="bg-blue-50 border border-blue-200 rounded-lg p-3 mb-3">
                <p class="text-sm text-blue-800">
                    💡 <strong>提示：</strong>此图表显示基于 Deno KV 持久化存储的历史数据。数据会在每次 API 请求后自动保存，并在 Deno Deploy 上永久保留（本地开发环境可能在重启后丢失）。
                </p>
            </div>

            <div class="mb-3 flex items-center gap-4">
                <div class="flex items-center gap-2">
                    <span class="text-sm text-gray-600">时间范围:</span>
                    <select id="time-range" class="px-3 py-1 border rounded-lg text-sm">
                        <option value="12">最近12个</option>
                        <option value="24" selected>最近24个</option>
                        <option value="48">最近48个</option>
                        <option value="72">最近72个</option>
                    </select>
                </div>
                <div class="text-sm text-gray-500" id="chart-subtitle">显示最近24小时的数据</div>
            </div>
            <canvas id="chart" height="80"></canvas>
        </div>

        <!-- Requests Table -->
        <div class="bg-white rounded-xl shadow-sm border p-6">
            <div class="flex items-center justify-between mb-4">
                <h2 class="text-xl font-bold text-gray-900">🔔 实时请求</h2>
                <span class="text-sm text-gray-500">自动刷新（每5秒）</span>
            </div>
            <div class="overflow-x-auto">
                <table class="w-full">
                    <thead>
                        <tr class="border-b">
                            <th class="text-left py-3 px-4 text-gray-700 font-semibold">时间</th>
                            <th class="text-left py-3 px-4 text-gray-700 font-semibold">方法</th>
                            <th class="text-left py-3 px-4 text-gray-700 font-semibold">路径</th>
                            <th class="text-left py-3 px-4 text-gray-700 font-semibold">模型</th>
                            <th class="text-left py-3 px-4 text-gray-700 font-semibold">状态</th>
                            <th class="text-left py-3 px-4 text-gray-700 font-semibold">耗时</th>
                        </tr>
                    </thead>
                    <tbody id="requests" class="divide-y"></tbody>
                </table>
            </div>
            <div id="empty" class="text-center py-8 text-gray-500 hidden">
                暂无请求记录
            </div>
            <!-- Pagination -->
            <div id="pagination" class="mt-4 flex items-center justify-between">
                <div class="flex items-center gap-4">
                    <div class="text-sm text-gray-600">
                        共 <span id="total-requests">0</span> 条记录，第 <span id="current-page">1</span> / <span id="total-pages">1</span> 页
                    </div>
                    <div class="flex items-center gap-2">
                        <span class="text-sm text-gray-600">每页:</span>
                        <select id="page-size" class="px-2 py-1 border rounded text-sm">
                            <option value="5">5</option>
                            <option value="10">10</option>
                            <option value="20" selected>20</option>
                            <option value="50">50</option>
                            <option value="100">100</option>
                        </select>
                    </div>
                </div>
                <div class="flex gap-2">
                    <button id="prev-page" class="px-3 py-1 bg-gray-200 hover:bg-gray-300 rounded disabled:opacity-50 disabled:cursor-not-allowed">上一页</button>
                    <button id="next-page" class="px-3 py-1 bg-gray-200 hover:bg-gray-300 rounded disabled:opacity-50 disabled:cursor-not-allowed">下一页</button>
                </div>
            </div>
        </div>
    </div>

    <script>
        let chart = null;
        const chartData = { labels: [], data: [] };
        let currentPage = 1;
        let pageSize = 20;
        let chartViewMode = 'hourly'; // 'hourly' or 'daily'
        let chartTimeRange = 24; // hours or days

        async function update() {
            try {
                const statsRes = await fetch('/dashboard/stats');
                const stats = await statsRes.json();

                // Top cards
                document.getElementById('total').textContent = stats.totalRequests;
                document.getElementById('success').textContent = stats.successfulRequests;
                document.getElementById('failed').textContent = stats.failedRequests;
                document.getElementById('avgtime').textContent = Math.round(stats.averageResponseTime) + 'ms';
                document.getElementById('homeviews').textContent = stats.homePageViews;

                // API Stats
                document.getElementById('api-calls').textContent = stats.apiCallsCount || 0;
                document.getElementById('models-calls').textContent = stats.modelsCallsCount || 0;
                document.getElementById('streaming').textContent = stats.streamingRequests || 0;
                document.getElementById('non-streaming').textContent = stats.nonStreamingRequests || 0;

                // Performance Stats
                document.getElementById('avg-time-detail').textContent = Math.round(stats.averageResponseTime) + 'ms';
                document.getElementById('fastest').textContent = stats.fastestResponse === Infinity ? '-' : Math.round(stats.fastestResponse) + 'ms';
                document.getElementById('slowest').textContent = stats.slowestResponse === 0 ? '-' : Math.round(stats.slowestResponse) + 'ms';
                const successRate = stats.totalRequests > 0 ? ((stats.successfulRequests / stats.totalRequests) * 100).toFixed(1) : '0';
                document.getElementById('success-rate').textContent = successRate + '%';

                // System Info
                const uptime = Date.now() - new Date(stats.startTime).getTime();
                const hours = Math.floor(uptime / 3600000);
                const minutes = Math.floor((uptime % 3600000) / 60000);
                document.getElementById('uptime').textContent = hours + 'h ' + minutes + 'm';
                document.getElementById('tokens').textContent = (stats.totalTokensUsed || 0).toLocaleString();
                document.getElementById('last-request').textContent = stats.lastRequestTime ? new Date(stats.lastRequestTime).toLocaleTimeString() : '-';
                document.getElementById('home-visits').textContent = stats.homePageViews;

                // Top Models
                const topModelsDiv = document.getElementById('top-models');
                if (stats.topModels && stats.topModels.length > 0) {
                    topModelsDiv.innerHTML = stats.topModels.map((m, i) => \`
                        <div class="flex items-center justify-between">
                            <div class="flex items-center gap-2">
                                <span class="text-lg">\${i === 0 ? '🥇' : i === 1 ? '🥈' : '🥉'}</span>
                                <span class="font-mono text-sm text-gray-700">\${m.model}</span>
                            </div>
                            <span class="font-bold text-purple-600">\${m.count}</span>
                        </div>
                    \`).join('');
                } else {
                    topModelsDiv.innerHTML = '<p class="text-gray-500 text-sm">暂无数据</p>';
                }

                // Fetch paginated requests
                const reqsRes = await fetch(\`/dashboard/requests?page=\${currentPage}&pageSize=\${pageSize}\`);
                const data = await reqsRes.json();
                const tbody = document.getElementById('requests');
                const empty = document.getElementById('empty');

                tbody.innerHTML = '';

                if (data.requests.length === 0) {
                    empty.classList.remove('hidden');
                } else {
                    empty.classList.add('hidden');
                    data.requests.forEach(r => {
                        const row = tbody.insertRow();
                        const time = new Date(r.timestamp).toLocaleTimeString();
                        const statusClass = r.status >= 200 && r.status < 300 ? 'text-green-600 bg-green-50' : 'text-red-600 bg-red-50';
                        const modelDisplay = r.model ? r.model : '-';

                        row.innerHTML = \`
                            <td class="py-3 px-4 text-gray-700">\${time}</td>
                            <td class="py-3 px-4"><span class="bg-blue-100 text-blue-700 px-2 py-1 rounded text-sm font-mono">\${r.method}</span></td>
                            <td class="py-3 px-4 font-mono text-sm text-gray-600">\${r.path}</td>
                            <td class="py-3 px-4 font-mono text-xs text-gray-600">\${modelDisplay}</td>
                            <td class="py-3 px-4"><span class="\${statusClass} px-2 py-1 rounded font-semibold text-sm">\${r.status}</span></td>
                            <td class="py-3 px-4 text-gray-700">\${r.duration}ms</td>
                        \`;
                    });

                    // Update pagination info
                    document.getElementById('total-requests').textContent = data.total;
                    document.getElementById('current-page').textContent = data.page;
                    document.getElementById('total-pages').textContent = data.totalPages;

                    // Enable/disable pagination buttons
                    document.getElementById('prev-page').disabled = data.page <= 1;
                    document.getElementById('next-page').disabled = data.page >= data.totalPages;
                }
            } catch (e) {
                console.error('Update error:', e);
            }
        }

        async function updateChartData() {
            try {
                let endpoint, labelKey, subtitle;

                if (chartViewMode === 'hourly') {
                    endpoint = \`/dashboard/hourly?hours=\${chartTimeRange}\`;
                    labelKey = 'hour';
                    subtitle = \`显示最近\${chartTimeRange}小时的数据\`;
                } else {
                    endpoint = \`/dashboard/daily?days=\${chartTimeRange}\`;
                    labelKey = 'date';
                    subtitle = \`显示最近\${chartTimeRange}天的数据\`;
                }

                const res = await fetch(endpoint);
                const data = await res.json();

                if (data && data.length > 0) {
                    chartData.labels = data.map(d => {
                        if (chartViewMode === 'hourly') {
                            // Format: 2025-09-30-14 -> 09-30 14:00
                            const parts = d[labelKey].split('-');
                            return \`\${parts[1]}-\${parts[2]} \${parts[3]}:00\`;
                        } else {
                            // Format: 2025-09-30 -> 09-30
                            const parts = d[labelKey].split('-');
                            return \`\${parts[1]}-\${parts[2]}\`;
                        }
                    });
                    chartData.data = data.map(d => Math.round(d.avgResponseTime));
                    subtitle += \` (共\${data.length}条记录)\`;
                } else {
                    chartData.labels = [];
                    chartData.data = [];
                    subtitle += ' - ⚠️ 暂无持久化数据，请发送API请求后稍等片刻';
                }

                document.getElementById('chart-subtitle').textContent = subtitle;
                updateChart();
            } catch (e) {
                console.error('Chart update error:', e);
                document.getElementById('chart-subtitle').textContent = '⚠️ 加载数据失败: ' + e.message;
            }
        }

        function updateChart() {
            const ctx = document.getElementById('chart').getContext('2d');

            if (chart) {
                chart.data.labels = chartData.labels;
                chart.data.datasets[0].data = chartData.data;
                chart.update();
            } else {
                chart = new Chart(ctx, {
                    type: 'line',
                    data: {
                        labels: chartData.labels,
                        datasets: [{
                            label: '响应时间 (ms)',
                            data: chartData.data,
                            borderColor: 'rgb(147, 51, 234)',
                            backgroundColor: 'rgba(147, 51, 234, 0.1)',
                            tension: 0.4,
                            fill: true
                        }]
                    },
                    options: {
                        responsive: true,
                        plugins: {
                            legend: { display: false },
                            tooltip: {
                                callbacks: {
                                    label: (ctx) => \`响应时间: \${ctx.parsed.y}ms\`
                                }
                            }
                        },
                        scales: {
                            y: {
                                beginAtZero: true,
                                ticks: { callback: (val) => val + 'ms' }
                            }
                        }
                    }
                });
            }
        }

        // Pagination handlers
        document.getElementById('prev-page').addEventListener('click', () => {
            if (currentPage > 1) {
                currentPage--;
                update();
            }
        });

        document.getElementById('next-page').addEventListener('click', () => {
            currentPage++;
            update();
        });

        // Chart view mode handlers
        document.getElementById('view-hourly').addEventListener('click', () => {
            chartViewMode = 'hourly';
            document.getElementById('view-hourly').className = 'px-4 py-2 bg-purple-600 text-white rounded-lg hover:bg-purple-700 transition text-sm font-semibold';
            document.getElementById('view-daily').className = 'px-4 py-2 bg-gray-200 text-gray-700 rounded-lg hover:bg-gray-300 transition text-sm font-semibold';
            updateChartData();
        });

        document.getElementById('view-daily').addEventListener('click', () => {
            chartViewMode = 'daily';
            document.getElementById('view-daily').className = 'px-4 py-2 bg-purple-600 text-white rounded-lg hover:bg-purple-700 transition text-sm font-semibold';
            document.getElementById('view-hourly').className = 'px-4 py-2 bg-gray-200 text-gray-700 rounded-lg hover:bg-gray-300 transition text-sm font-semibold';
            updateChartData();
        });

        // Time range handler
        document.getElementById('time-range').addEventListener('change', (e) => {
            chartTimeRange = parseInt(e.target.value);
            updateChartData();
        });

        // Page size handler
        document.getElementById('page-size').addEventListener('change', (e) => {
            pageSize = parseInt(e.target.value);
            currentPage = 1; // Reset to first page
            update();
        });

        update();
        updateChartData();
        setInterval(update, 5000);
        setInterval(updateChartData, 60000); // Update chart every minute
    </script>
</body>
</html>`;

// Deploy guide HTML
const deployHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>部署指南 - ZtoApi</title>
    <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="bg-gray-50">
    <nav class="bg-white shadow-sm border-b">
        <div class="container mx-auto px-4 py-4">
            <div class="flex items-center justify-between">
                <a href="/" class="flex items-center space-x-2 text-purple-600 hover:text-purple-700 transition">
                    <span class="text-2xl">🦕</span>
                    <span class="text-xl font-bold">ZtoApi</span>
                </a>
                <div class="flex space-x-4">
                    <a href="/" class="text-gray-600 hover:text-purple-600 transition">首页</a>
                    <a href="/docs" class="text-gray-600 hover:text-purple-600 transition">文档</a>
                    <a href="/deploy" class="text-purple-600 font-semibold">部署</a>
                    <a href="/dashboard" class="text-gray-600 hover:text-purple-600 transition">Dashboard</a>
                </div>
            </div>
        </div>
    </nav>

    <div class="container mx-auto px-4 py-8 max-w-5xl">
        <div class="text-center mb-12">
            <h1 class="text-4xl font-bold text-gray-900 mb-3">🚀 Deno Deploy 部署指南</h1>
            <p class="text-gray-600">快速部署到 Deno Deploy 平台</p>
        </div>

        <!-- Current Deployment -->
        <div class="bg-gradient-to-r from-purple-600 to-indigo-600 rounded-xl p-8 mb-8 text-white">
            <div class="flex items-center space-x-4 mb-4">
                <span class="text-4xl">🌐</span>
                <div>
                    <h2 class="text-2xl font-bold mb-2">当前部署地址</h2>
                    <a href="https://zto2api.deno.dev" target="_blank" class="text-white/90 hover:text-white underline text-lg font-mono">
                        https://zto2api.deno.dev
                    </a>
                </div>
            </div>
            <p class="text-white/80">✅ 已部署并运行中</p>
        </div>

        <!-- Quick Start -->
        <div class="bg-white rounded-xl shadow-sm border p-8 mb-6">
            <h2 class="text-2xl font-bold text-gray-900 mb-6 flex items-center">
                <span class="mr-3">⚡</span> 快速开始
            </h2>
            <div class="space-y-4">
                <div class="flex items-start">
                    <span class="bg-purple-100 text-purple-700 rounded-full w-8 h-8 flex items-center justify-center font-bold mr-4 flex-shrink-0">1</span>
                    <div class="flex-1">
                        <h3 class="font-semibold text-gray-900 mb-2">Fork 仓库</h3>
                        <p class="text-gray-600">前往 <a href="https://github.com/libaxuan/ZtoApi" target="_blank" class="text-purple-600 hover:underline">GitHub 仓库</a>，点击右上角的 Fork 按钮</p>
                    </div>
                </div>

                <div class="flex items-start">
                    <span class="bg-purple-100 text-purple-700 rounded-full w-8 h-8 flex items-center justify-center font-bold mr-4 flex-shrink-0">2</span>
                    <div class="flex-1">
                        <h3 class="font-semibold text-gray-900 mb-2">登录 Deno Deploy</h3>
                        <p class="text-gray-600 mb-2">访问 <a href="https://dash.deno.com" target="_blank" class="text-purple-600 hover:underline">Deno Deploy</a> 并使用 GitHub 账号登录</p>
                    </div>
                </div>

                <div class="flex items-start">
                    <span class="bg-purple-100 text-purple-700 rounded-full w-8 h-8 flex items-center justify-center font-bold mr-4 flex-shrink-0">3</span>
                    <div class="flex-1">
                        <h3 class="font-semibold text-gray-900 mb-2">创建新项目</h3>
                        <p class="text-gray-600">点击 "New Project"，选择你 Fork 的仓库，入口文件选择 <code class="bg-gray-100 px-2 py-1 rounded font-mono text-sm">main.ts</code></p>
                    </div>
                </div>

                <div class="flex items-start">
                    <span class="bg-purple-100 text-purple-700 rounded-full w-8 h-8 flex items-center justify-center font-bold mr-4 flex-shrink-0">4</span>
                    <div class="flex-1">
                        <h3 class="font-semibold text-gray-900 mb-2">配置环境变量</h3>
                        <p class="text-gray-600">在项目设置中添加必要的环境变量（见下方详细说明）</p>
                    </div>
                </div>

                <div class="flex items-start">
                    <span class="bg-purple-100 text-purple-700 rounded-full w-8 h-8 flex items-center justify-center font-bold mr-4 flex-shrink-0">5</span>
                    <div class="flex-1">
                        <h3 class="font-semibold text-gray-900 mb-2">部署完成！</h3>
                        <p class="text-gray-600">Deno Deploy 会自动部署，几秒钟后即可访问</p>
                    </div>
                </div>
            </div>
        </div>

        <!-- Environment Variables -->
        <div class="bg-white rounded-xl shadow-sm border p-8 mb-6">
            <h2 class="text-2xl font-bold text-gray-900 mb-6 flex items-center">
                <span class="mr-3">🔐</span> 环境变量配置
            </h2>

            <div class="space-y-6">
                <!-- ZAI_TOKEN -->
                <div class="border-l-4 border-yellow-400 bg-yellow-50 p-4 rounded-r-lg">
                    <h3 class="font-bold text-gray-900 mb-2 flex items-center">
                        <code class="bg-yellow-100 px-2 py-1 rounded mr-2 text-sm">ZAI_TOKEN</code>
                        <span class="text-yellow-600 text-sm">(可选)</span>
                    </h3>
                    <p class="text-gray-700 mb-3">Z.ai 的访问令牌。如不设置，系统会自动获取匿名 token。</p>
                    <div class="bg-white rounded p-3 mb-2">
                        <p class="font-semibold text-gray-800 mb-2">获取方式：</p>
                        <ol class="list-decimal list-inside space-y-1 text-sm text-gray-600">
                            <li>访问 <a href="https://chat.z.ai" target="_blank" class="text-purple-600 hover:underline">chat.z.ai</a> 并登录</li>
                            <li>打开浏览器开发者工具（F12）</li>
                            <li>切换到 Network 标签</li>
                            <li>发送一条消息</li>
                            <li>在请求头中找到 <code class="bg-gray-100 px-1 rounded">Authorization: Bearer ...</code></li>
                            <li>复制 Bearer 后面的 token</li>
                        </ol>
                    </div>
                    <p class="text-sm text-yellow-700">💡 如果使用匿名 token，每次请求都会创建新的会话，不会保留历史记录</p>
                </div>

                <!-- DEFAULT_KEY -->
                <div class="border-l-4 border-blue-400 bg-blue-50 p-4 rounded-r-lg">
                    <h3 class="font-bold text-gray-900 mb-2 flex items-center">
                        <code class="bg-blue-100 px-2 py-1 rounded mr-2 text-sm">DEFAULT_KEY</code>
                        <span class="text-blue-600 text-sm">(推荐设置)</span>
                    </h3>
                    <p class="text-gray-700 mb-2">客户端调用 API 时需要的密钥。</p>
                    <div class="bg-white rounded p-3">
                        <p class="text-sm text-gray-600 mb-1">默认值：<code class="bg-gray-100 px-2 py-1 rounded font-mono">sk-your-key</code></p>
                        <p class="text-sm text-blue-700">🔒 建议设置为复杂的随机字符串，例如：<code class="bg-gray-100 px-2 py-1 rounded font-mono text-xs">sk-1a2b3c4d5e6f...</code></p>
                    </div>
                </div>

                <!-- Other Variables -->
                <div class="border-l-4 border-gray-400 bg-gray-50 p-4 rounded-r-lg">
                    <h3 class="font-bold text-gray-900 mb-3">其他可选变量</h3>
                    <div class="space-y-2 text-sm">
                        <div class="bg-white rounded p-2">
                            <code class="text-purple-600 font-mono">MODEL_NAME</code>
                            <span class="text-gray-600 ml-2">- 模型显示名称（默认：GLM-4.5）</span>
                        </div>
                        <div class="bg-white rounded p-2">
                            <code class="text-purple-600 font-mono">DEBUG_MODE</code>
                            <span class="text-gray-600 ml-2">- 调试模式（默认：true）</span>
                        </div>
                        <div class="bg-white rounded p-2">
                            <code class="text-purple-600 font-mono">DEFAULT_STREAM</code>
                            <span class="text-gray-600 ml-2">- 默认流式响应（默认：true）</span>
                        </div>
                        <div class="bg-white rounded p-2">
                            <code class="text-purple-600 font-mono">ENABLE_THINKING</code>
                            <span class="text-gray-600 ml-2">- 启用思考功能（默认：false）</span>
                        </div>
                        <div class="bg-white rounded p-2">
                            <code class="text-purple-600 font-mono">DASHBOARD_ENABLED</code>
                            <span class="text-gray-600 ml-2">- 启用 Dashboard（默认：true）</span>
                        </div>
                    </div>
                </div>
            </div>
        </div>

        <!-- Usage Example -->
        <div class="bg-white rounded-xl shadow-sm border p-8 mb-6">
            <h2 class="text-2xl font-bold text-gray-900 mb-6 flex items-center">
                <span class="mr-3">💻</span> 使用示例
            </h2>

            <p class="text-gray-700 mb-4">部署完成后，使用以下代码调用 API：</p>

            <div class="bg-gray-900 rounded-lg p-4 overflow-x-auto mb-4">
                <pre class="text-green-400 font-mono text-sm">import openai

client = openai.OpenAI(
    api_key="你设置的 DEFAULT_KEY",
    base_url="https://zto2api.deno.dev/v1"
)

response = client.chat.completions.create(
    model="GLM-4.5",
    messages=[{"role": "user", "content": "你好"}]
)

print(response.choices[0].message.content)</pre>
            </div>

            <div class="bg-blue-50 border border-blue-200 rounded-lg p-4">
                <p class="text-sm text-blue-800">
                    <strong>提示：</strong> 确保使用你在环境变量中设置的 <code class="bg-white px-2 py-1 rounded">DEFAULT_KEY</code> 作为 api_key
                </p>
            </div>
        </div>

        <!-- Tips -->
        <div class="bg-gradient-to-r from-green-50 to-blue-50 border border-green-200 rounded-xl p-6 mb-6">
            <h3 class="font-bold text-gray-900 mb-3 flex items-center">
                <span class="text-2xl mr-2">💡</span> 小贴士
            </h3>
            <ul class="space-y-2 text-gray-700">
                <li class="flex items-start">
                    <span class="text-green-600 mr-2">✓</span>
                    <span>Deno Deploy 提供免费额度，适合个人使用</span>
                </li>
                <li class="flex items-start">
                    <span class="text-green-600 mr-2">✓</span>
                    <span>每次 git push 后会自动重新部署</span>
                </li>
                <li class="flex items-start">
                    <span class="text-green-600 mr-2">✓</span>
                    <span>支持自定义域名（在项目设置中配置）</span>
                </li>
                <li class="flex items-start">
                    <span class="text-green-600 mr-2">✓</span>
                    <span>可在 Deno Deploy 控制台查看日志和监控</span>
                </li>
            </ul>
        </div>

        <!-- Actions -->
        <div class="flex justify-center space-x-4">
            <a href="https://dash.deno.com/new" target="_blank" class="inline-block bg-purple-600 hover:bg-purple-700 text-white font-semibold px-8 py-3 rounded-lg transition">
                立即部署到 Deno Deploy
            </a>
            <a href="/" class="inline-block bg-gray-200 hover:bg-gray-300 text-gray-700 font-semibold px-8 py-3 rounded-lg transition">
                返回首页
            </a>
        </div>
    </div>
</body>
</html>`;

// Home page HTML
const homeHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>ZtoApi - OpenAI兼容API代理</title>
    <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="min-h-screen bg-gradient-to-br from-purple-600 via-purple-700 to-indigo-800">
    <div class="container mx-auto px-4 py-12 flex items-center justify-center min-h-screen">
        <div class="max-w-4xl w-full">
            <!-- Header -->
            <div class="text-center mb-12 animate-fade-in">
                <h1 class="text-6xl font-bold text-white mb-4">
                    <span class="inline-block hover:scale-110 transition-transform">🦕</span> ZtoApi
                </h1>
                <p class="text-xl text-purple-100">OpenAI 兼容 API 代理 for Z.ai GLM-4.5</p>
            </div>

            <!-- Status Card -->
            <div class="bg-white/10 backdrop-blur-lg rounded-2xl p-8 mb-8 border border-white/20 shadow-2xl">
                <div class="grid grid-cols-2 md:grid-cols-4 gap-6">
                    <div class="text-center">
                        <div class="text-3xl mb-2">🟢</div>
                        <div class="text-white/60 text-sm mb-1">状态</div>
                        <div class="text-white font-semibold">运行中</div>
                    </div>
                    <div class="text-center">
                        <div class="text-3xl mb-2">🤖</div>
                        <div class="text-white/60 text-sm mb-1">模型</div>
                        <div class="text-white font-semibold font-mono">${MODEL_NAME}</div>
                    </div>
                    <div class="text-center">
                        <div class="text-3xl mb-2">🔌</div>
                        <div class="text-white/60 text-sm mb-1">端口</div>
                        <div class="text-white font-semibold font-mono">${PORT}</div>
                    </div>
                    <div class="text-center">
                        <div class="text-3xl mb-2">⚡</div>
                        <div class="text-white/60 text-sm mb-1">运行时</div>
                        <div class="text-white font-semibold">Deno</div>
                    </div>
                </div>
            </div>

            <!-- Navigation Cards -->
            <div class="grid md:grid-cols-4 gap-6 mb-8">
                <a href="/docs" class="group bg-white/10 backdrop-blur-lg rounded-xl p-6 border border-white/20 hover:bg-white/20 hover:border-white/40 transition-all duration-300 hover:-translate-y-2 hover:shadow-2xl">
                    <div class="text-5xl mb-4 group-hover:scale-110 transition-transform">📖</div>
                    <h3 class="text-white text-xl font-bold mb-2">API 文档</h3>
                    <p class="text-purple-100">查看完整的 API 使用文档和示例</p>
                </a>

                <a href="/deploy" class="group bg-white/10 backdrop-blur-lg rounded-xl p-6 border border-white/20 hover:bg-white/20 hover:border-white/40 transition-all duration-300 hover:-translate-y-2 hover:shadow-2xl">
                    <div class="text-5xl mb-4 group-hover:scale-110 transition-transform">🚀</div>
                    <h3 class="text-white text-xl font-bold mb-2">部署指南</h3>
                    <p class="text-purple-100">快速部署到 Deno Deploy</p>
                </a>

                <a href="/dashboard" class="group bg-white/10 backdrop-blur-lg rounded-xl p-6 border border-white/20 hover:bg-white/20 hover:border-white/40 transition-all duration-300 hover:-translate-y-2 hover:shadow-2xl">
                    <div class="text-5xl mb-4 group-hover:scale-110 transition-transform">📊</div>
                    <h3 class="text-white text-xl font-bold mb-2">Dashboard</h3>
                    <p class="text-purple-100">实时监控请求和性能统计</p>
                </a>

                <a href="/v1/models" class="group bg-white/10 backdrop-blur-lg rounded-xl p-6 border border-white/20 hover:bg-white/20 hover:border-white/40 transition-all duration-300 hover:-translate-y-2 hover:shadow-2xl">
                    <div class="text-5xl mb-4 group-hover:scale-110 transition-transform">🤖</div>
                    <h3 class="text-white text-xl font-bold mb-2">模型列表</h3>
                    <p class="text-purple-100">查看所有可用的 AI 模型</p>
                </a>
            </div>

            <!-- Footer -->
            <div class="text-center text-white/60 text-sm space-y-3">
                <p>Powered by <span class="font-semibold text-white">Deno 🦕</span> | OpenAI Compatible API</p>
                <div class="flex justify-center items-center gap-6 text-xs">
                    <a href="https://github.com/dext7r/ZtoApi" target="_blank" rel="noopener noreferrer" class="hover:text-white transition-colors">
                        📦 项目地址
                    </a>
                    <span class="text-white/40">|</span>
                    <a href="https://github.com/libaxuan/ZtoApi" target="_blank" rel="noopener noreferrer" class="hover:text-white transition-colors">
                        🔗 原仓库
                    </a>
                    <span class="text-white/40">|</span>
                    <a href="https://linux.do/t/topic/1000335" target="_blank" rel="noopener noreferrer" class="hover:text-white transition-colors">
                        💬 交流讨论
                    </a>
                </div>
                <p class="text-white/50 text-xs italic pt-2">欲买桂花同载酒 终不似 少年游</p>
            </div>
        </div>
    </div>
</body>
</html>`;

// API docs HTML
const apiDocsHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>API Documentation - ZtoApi</title>
    <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="bg-gray-50">
    <nav class="bg-white shadow-sm border-b">
        <div class="container mx-auto px-4 py-4">
            <div class="flex items-center justify-between">
                <a href="/" class="flex items-center space-x-2 text-purple-600 hover:text-purple-700 transition">
                    <span class="text-2xl">🦕</span>
                    <span class="text-xl font-bold">ZtoApi</span>
                </a>
                <div class="flex space-x-4">
                    <a href="/" class="text-gray-600 hover:text-purple-600 transition">首页</a>
                    <a href="/docs" class="text-purple-600 font-semibold">文档</a>
                    <a href="/deploy" class="text-gray-600 hover:text-purple-600 transition">部署</a>
                    <a href="/dashboard" class="text-gray-600 hover:text-purple-600 transition">Dashboard</a>
                </div>
            </div>
        </div>
    </nav>

    <div class="container mx-auto px-4 py-8 max-w-5xl">
        <div class="text-center mb-12">
            <h1 class="text-4xl font-bold text-gray-900 mb-3">📖 API Documentation</h1>
            <p class="text-gray-600">OpenAI 兼容的 API 接口文档</p>
        </div>

        <div class="bg-white rounded-xl shadow-sm border p-8 mb-6">
            <h2 class="text-2xl font-bold text-gray-900 mb-4">概述</h2>
            <p class="text-gray-700 mb-4">ZtoApi 是一个为 Z.ai GLM-4.5 模型提供 OpenAI 兼容 API 接口的代理服务器。</p>
            <div class="bg-purple-50 border border-purple-200 rounded-lg p-4">
                <p class="text-sm text-gray-600 mb-2">基础 URL</p>
                <code class="text-purple-700 font-mono text-lg">https://zto2api.deno.dev/v1</code>
            </div>
        </div>

        <div class="bg-white rounded-xl shadow-sm border p-8 mb-6">
            <h2 class="text-2xl font-bold text-gray-900 mb-4">🔐 身份验证</h2>
            <p class="text-gray-700 mb-4">所有 API 请求都需要在请求头中包含 Bearer Token：</p>
            <div class="bg-gray-900 rounded-lg p-4 overflow-x-auto">
                <code class="text-green-400 font-mono text-sm">Authorization: Bearer ${DEFAULT_KEY}</code>
            </div>
        </div>

        <div class="bg-white rounded-xl shadow-sm border p-8 mb-6">
            <h2 class="text-2xl font-bold text-gray-900 mb-6">🔌 API 端点</h2>

            <div class="mb-8">
                <div class="flex items-center space-x-3 mb-3">
                    <span class="bg-green-100 text-green-700 px-3 py-1 rounded-lg font-semibold text-sm">GET</span>
                    <code class="text-lg font-mono text-gray-800">/v1/models</code>
                </div>
                <p class="text-gray-700 mb-3">获取可用模型列表</p>
                <div class="bg-gray-900 rounded-lg p-4 overflow-x-auto">
                    <pre class="text-green-400 font-mono text-sm">curl https://zto2api.deno.dev/v1/models \\
  -H "Authorization: Bearer ${DEFAULT_KEY}"</pre>
                </div>
            </div>

            <div>
                <div class="flex items-center space-x-3 mb-3">
                    <span class="bg-blue-100 text-blue-700 px-3 py-1 rounded-lg font-semibold text-sm">POST</span>
                    <code class="text-lg font-mono text-gray-800">/v1/chat/completions</code>
                </div>
                <p class="text-gray-700 mb-4">创建聊天完成（支持流式和非流式）</p>

                <div class="bg-gray-50 rounded-lg p-4 mb-4">
                    <h4 class="font-semibold text-gray-900 mb-3">请求参数</h4>
                    <div class="space-y-2 text-sm">
                        <div class="flex items-start">
                            <code class="bg-white px-2 py-1 rounded mr-3 text-purple-600 font-mono">model</code>
                            <span class="text-gray-600">string, 必需 - 模型名称 (如 "${MODEL_NAME}")</span>
                        </div>
                        <div class="flex items-start">
                            <code class="bg-white px-2 py-1 rounded mr-3 text-purple-600 font-mono">messages</code>
                            <span class="text-gray-600">array, 必需 - 消息列表</span>
                        </div>
                        <div class="flex items-start">
                            <code class="bg-white px-2 py-1 rounded mr-3 text-purple-600 font-mono">stream</code>
                            <span class="text-gray-600">boolean, 可选 - 是否流式响应（默认: ${DEFAULT_STREAM}）</span>
                        </div>
                        <div class="flex items-start">
                            <code class="bg-white px-2 py-1 rounded mr-3 text-purple-600 font-mono">enable_thinking</code>
                            <span class="text-gray-600">boolean, 可选 - 是否启用思考功能</span>
                        </div>
                    </div>
                </div>

                <h4 class="font-semibold text-gray-900 mb-3">请求示例</h4>
                <div class="bg-gray-900 rounded-lg p-4 overflow-x-auto">
                    <pre class="text-green-400 font-mono text-sm">curl -X POST https://zto2api.deno.dev/v1/chat/completions \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer ${DEFAULT_KEY}" \\
  -d '{
    "model": "${MODEL_NAME}",
    "messages": [
      {"role": "user", "content": "你好"}
    ],
    "stream": false
  }'</pre>
                </div>
            </div>
        </div>

        <div class="bg-white rounded-xl shadow-sm border p-8 mb-6">
            <h2 class="text-2xl font-bold text-gray-900 mb-4">🐍 Python 示例</h2>
            <div class="bg-gray-900 rounded-lg p-4 overflow-x-auto">
                <pre class="text-green-400 font-mono text-sm">import openai

client = openai.OpenAI(
    api_key="${DEFAULT_KEY}",
    base_url="https://zto2api.deno.dev/v1"
)

response = client.chat.completions.create(
    model="${MODEL_NAME}",
    messages=[{"role": "user", "content": "你好"}]
)

print(response.choices[0].message.content)</pre>
            </div>
        </div>

        <div class="text-center">
            <a href="/" class="inline-block bg-purple-600 hover:bg-purple-700 text-white font-semibold px-6 py-3 rounded-lg transition">
                返回首页
            </a>
        </div>
    </div>
</body>
</html>`;

// Main request handler
async function handler(req: Request): Promise<Response> {
  const url = new URL(req.url);
  const path = url.pathname;

  // CORS preflight
  if (req.method === "OPTIONS") {
    const headers = new Headers();
    setCORSHeaders(headers);
    return new Response(null, { status: 200, headers });
  }

  // Routes
  if (path === "/" && req.method === "GET") {
    stats.homePageViews++;
    return new Response(homeHTML, {
      status: 200,
      headers: { "Content-Type": "text/html; charset=utf-8" },
    });
  }

  if (path === "/v1/models" && req.method === "GET") {
    return await handleModels(req);
  }

  if (path === "/v1/chat/completions" && req.method === "POST") {
    return await handleChatCompletions(req);
  }

  if (path === "/docs" && req.method === "GET") {
    return new Response(apiDocsHTML, {
      status: 200,
      headers: { "Content-Type": "text/html; charset=utf-8" },
    });
  }

  if (path === "/deploy" && req.method === "GET") {
    return new Response(deployHTML, {
      status: 200,
      headers: { "Content-Type": "text/html; charset=utf-8" },
    });
  }

  if (DASHBOARD_ENABLED) {
    if (path === "/dashboard" && req.method === "GET") {
      return new Response(dashboardHTML, {
        status: 200,
        headers: { "Content-Type": "text/html; charset=utf-8" },
      });
    }

    if (path === "/dashboard/stats" && req.method === "GET") {
      // Get top 3 models
      const modelEntries = Array.from(stats.modelUsage.entries())
        .sort((a, b) => b[1] - a[1])
        .slice(0, 3);
      const topModels = modelEntries.map(([model, count]) => ({ model, count }));

      // Convert stats to JSON-serializable format
      const statsResponse = {
        ...stats,
        modelUsage: undefined, // Remove Map
        topModels, // Add top 3 models
      };

      return new Response(JSON.stringify(statsResponse), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }

    if (path === "/dashboard/requests" && req.method === "GET") {
      const page = parseInt(url.searchParams.get("page") || "1");
      const pageSize = parseInt(url.searchParams.get("pageSize") || "20");
      const start = (page - 1) * pageSize;
      const end = start + pageSize;

      const paginatedRequests = liveRequests.slice().reverse().slice(start, end);

      return new Response(JSON.stringify({
        requests: paginatedRequests,
        total: liveRequests.length,
        page,
        pageSize,
        totalPages: Math.ceil(liveRequests.length / pageSize),
      }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }

    if (path === "/dashboard/hourly" && req.method === "GET") {
      const hours = parseInt(url.searchParams.get("hours") || "24");
      const hourlyStats = await getHourlyStats(hours);
      return new Response(JSON.stringify(hourlyStats), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }

    if (path === "/dashboard/daily" && req.method === "GET") {
      const days = parseInt(url.searchParams.get("days") || "30");
      const dailyStats = await getDailyStats(days);
      return new Response(JSON.stringify(dailyStats), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
  }

  return new Response("Not Found", { status: 404 });
}

// Start server
console.log(`🚀 ZtoApi (Deno) starting on port ${PORT}`);
console.log(`📦 Model: ${MODEL_NAME}`);
console.log(`🌐 Upstream: ${UPSTREAM_URL}`);
console.log(`🐛 Debug mode: ${DEBUG_MODE}`);
console.log(`🌊 Default stream: ${DEFAULT_STREAM}`);
console.log(`📊 Dashboard enabled: ${DASHBOARD_ENABLED}`);
console.log(`🧠 Thinking enabled: ${ENABLE_THINKING}`);
if (DASHBOARD_ENABLED) {
  console.log(`📊 Dashboard: http://localhost:${PORT}/dashboard`);
}
console.log(`📖 API Docs: http://localhost:${PORT}/docs`);

// Initialize database and start cleanup task
await initDB();

// Schedule daily stats aggregation and cleanup (runs every hour)
setInterval(async () => {
  await saveDailyStats();
  await cleanupOldData();
}, 60 * 60 * 1000);

Deno.serve({ port: PORT }, handler);
