# Setu examples

Start Setu first (the `mock` provider needs no keys):

```bash
echo 'model_list: [{model_name: mock, provider: mock, params: {}}]' > config.yaml
setu --config config.yaml
```

## curl

```bash
# non-streaming
curl -s http://localhost:4000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"mock","messages":[{"role":"user","content":"hello"}]}'

# streaming
curl -sN http://localhost:4000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"mock","stream":true,"messages":[{"role":"user","content":"hello"}]}'
```

## Python (official OpenAI SDK)

```python
# pip install openai
from openai import OpenAI

client = OpenAI(base_url="http://localhost:4000/v1", api_key="unused")

# non-streaming
resp = client.chat.completions.create(
    model="mock",
    messages=[{"role": "user", "content": "hello from python"}],
)
print(resp.choices[0].message.content)

# streaming
for chunk in client.chat.completions.create(
    model="mock",
    messages=[{"role": "user", "content": "stream please"}],
    stream=True,
):
    print(chunk.choices[0].delta.content or "", end="", flush=True)
```

## Node (official openai package)

```js
// npm i openai
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "http://localhost:4000/v1", apiKey: "unused" });

const resp = await client.chat.completions.create({
  model: "mock",
  messages: [{ role: "user", content: "hello from node" }],
});
console.log(resp.choices[0].message.content);
```

Swap `model: "mock"` for `"gpt-4o"`, `"claude"`, or any model name in your
`config.yaml` — the client code doesn't change.
