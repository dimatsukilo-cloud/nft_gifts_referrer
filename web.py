import os
from flask import Flask

app = Flask(__name__)

@app.route("/")
def home():
    return "Bot is running!", 200

@app.route("/health")
def health():
    return "OK", 200

def run_web():
    port = int(os.environ.get("PORT", 10000))
    app.run(host="0.0.0.0", port=port)