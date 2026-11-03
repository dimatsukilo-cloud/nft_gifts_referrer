import os
from flask import Flask

app = Flask(name)

@app.route("/")
def home():
return "Bot is running!", 200

@app.route("/health")
def health():
return "OK", 200

if name == "main":
port = int(os.environ.get("PORT", 10000))
app.run(host="0.0.0.0", port=port)