import { query } from "@anthropic-ai/claude-agent-sdk";

const BASE = "http://localhost:17823";

async function test(name, envExtra) {
  console.log(`\n\n========== TEST: ${name} ==========`);
  const env = {
    ...process.env,
    ANTHROPIC_BASE_URL: BASE,
    // clean slate
    ANTHROPIC_API_KEY: undefined,
    ANTHROPIC_AUTH_TOKEN: undefined,
    CLAUDE_CODE_OAUTH_TOKEN: undefined,
    ...envExtra,
  };
  // Remove undefined keys so they don't leak
  for (const k of ["ANTHROPIC_API_KEY","ANTHROPIC_AUTH_TOKEN","CLAUDE_CODE_OAUTH_TOKEN"]) {
    if (env[k] === undefined) delete env[k];
  }
  console.log("env:", envExtra);
  try {
    const q = query({
      prompt: "say hello one word",
      options: {
        model: "sonnet",
        env,
        cwd: process.cwd(),
        allowDangerouslySkipPermissions: true,
      }
    });
    let count = 0;
    for await (const msg of q) {
      console.log(`msg ${++count}: type=${msg.type} subtype=${msg.subtype || '-'}`);
      if (msg.type === "result") {
        console.log("result:", JSON.stringify(msg).slice(0,2000));
        break;
      }
      if (count > 10) break;
    }
    console.log(`done test ${name}`);
  } catch (e) {
    console.error(`error in ${name}:`, e);
  }
  // small pause
  await new Promise(r => setTimeout(r, 1500));
}

// sequential
await test("1-CLAUDE_CODE_OAUTH_TOKEN", { CLAUDE_CODE_OAUTH_TOKEN: "sk-ant-oat01-test-dummy-123456" });
await test("2-ANTHROPIC_API_KEY", { ANTHROPIC_API_KEY: "sk-ant-api03-test-dummy-789" });
await test("3-ANTHROPIC_AUTH_TOKEN", { ANTHROPIC_AUTH_TOKEN: "test-auth-token-xyz", ANTHROPIC_API_KEY: "sk-ant-api03-dummy2" });
await test("4-OAUTH+API_KEY (api wins?)", { CLAUDE_CODE_OAUTH_TOKEN: "sk-ant-oat01-test", ANTHROPIC_API_KEY: "sk-ant-api03-test" });

console.log("\nAll tests done. Check capture.log");
process.exit(0);
