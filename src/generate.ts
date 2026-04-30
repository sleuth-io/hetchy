import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic();

export async function generateCode(prompt: string): Promise<string> {
  const message = await client.messages.create({
    model: "claude-opus-4-5",
    max_tokens: 4096,
    messages: [
      {
        role: "user",
        content: prompt,
      },
    ],
  });

  const content = message.content[0];
  if (content.type !== "text") {
    throw new Error("Unexpected response type from Claude API");
  }

  return content.text;
}
