#!/usr/bin/env python3
"""
Dictation audio generation helpers.

Normal dictation flow imports generate_dictation_audio() after selecting words
with vocab_manager.py due. By default, TTS 1.0 generates normal-speed prompts
and slow paired targets separately, then joins them with deterministic silence.
The contextual TTS 2.0 whole-segment mode remains available as a fallback.
"""

import base64
import uuid
# POSIX-only (Linux production); on Windows/macOS dev host fcntl is absent.
# Mirrors the Go-side convention in pkg/pybridge/bridge_linux.go (real impl) +
# bridge_other.go (documented no-op stub). See CLAUDE.md cross-platform dev principle.
try:
    import fcntl  # POSIX only (Linux production)
except ImportError:
    fcntl = None  # Windows/macOS dev host — see CLAUDE.md cross-platform dev principle
import hashlib
import os
import shutil
import sys
import json
import subprocess
import tempfile
import time
import datetime
import asyncio
import argparse
from zoneinfo import ZoneInfo

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
SKILL_DIR = os.path.dirname(SCRIPT_DIR)


# ─── Env Loading ───
_ENV_LOADED = False


def _load_env():
    global _ENV_LOADED
    if _ENV_LOADED:
        return

    candidates = []
    if os.environ.get("DICTATION_ENV_FILE"):
        candidates.append(os.environ["DICTATION_ENV_FILE"])
    candidates.extend([
        os.environ.get("DICTATION_ENV_FILE", ""),
        os.path.join(SKILL_DIR, ".env"),
        os.path.join(os.getcwd(), ".env"),
    ])
    seen = set()
    while candidates:
        env_path = candidates.pop(0)
        if not env_path or env_path in seen:
            continue
        seen.add(env_path)
        if os.path.exists(env_path):
            with open(env_path, encoding="utf-8") as f:
                for line in f:
                    line = line.strip()
                    if line and not line.startswith("#") and "=" in line:
                        k, v = line.split("=", 1)
                        os.environ.setdefault(k.strip(), v.strip())
            nested = os.environ.get("DICTATION_ENV_FILE")
            if nested and nested not in seen:
                candidates.append(nested)
    _ENV_LOADED = True


_load_env()

# ─── Constants ───
DEFAULT_DATA_DIR = os.environ.get("DICTATION_DATA_DIR", os.path.join(SKILL_DIR, "data"))
PYTHON = os.environ.get("DICTATION_PYTHON", sys.executable)
VOCAB_MANAGER = os.path.join(SCRIPT_DIR, "vocab_manager.py")
CACHE_DIR = os.environ.get("DICTATION_CACHE_DIR", os.path.join(DEFAULT_DATA_DIR, "cache", "audio"))
MANIFEST_DIR = os.environ.get("DICTATION_MANIFEST_DIR", os.path.join(DEFAULT_DATA_DIR, "cache"))
VOLCENGINE_TTS_URL = "https://openspeech.bytedance.com/api/v3/tts/unidirectional"
VOLCENGINE_TTS_MODEL = "seed-tts-2.0-expressive"

SILENCE_AFTER_INTRO = 0.8
SILENCE_BETWEEN_REPEATS = 0.5
SILENCE_AFTER_WORD = 1.75
MAX_INTROS = 30
CONTEXT_PROMPT_VERSION = "classroom-dictation-v3"

ENGLISH_CONTEXT_INSTRUCTION = (
    "This is an elementary-school English spelling dictation. Speak the opening, "
    "instructions, item numbers, and closing at a normal conversational pace. "
    "Pronounce each target word or phrase slowly and clearly exactly twice. "
    "After the second pronunciation of every target, you must remain completely "
    "silent for about two full seconds before saying the next item number. Do not "
    "speak a placeholder or count those two seconds aloud. "
    "Use a patient, neutral classroom-teacher tone without dramatic expression."
)

CHINESE_CONTEXT_INSTRUCTION = (
    "这是小学语文课堂的词语默写。开场、提示语、序号和结束语使用正常语速。"
    "每个需要默写的目标词语放慢语速，清晰、准确地朗读两遍。"
    "每组目标词语第二遍读完后，必须保持完整静音约两秒，再读下一个序号。"
    "静音期间不要朗读占位词，也不要把两秒计数说出来。"
    "使用耐心、中性、自然的课堂教师语气，不要夸张演绎。"
)

NUMBER_WORDS = [
    "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
    "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen",
    "eighteen", "nineteen", "twenty", "twenty-one", "twenty-two", "twenty-three",
    "twenty-four", "twenty-five", "twenty-six", "twenty-seven", "twenty-eight",
    "twenty-nine", "thirty",
]

# ─── TTS Configs ───
ENGLISH_CONFIG = {
    "active": "volcengine",
    "generation_mode": "fragments",
    "resource_id": "seed-tts-1.0",
    "model": "seed-tts-1.0",
    "fragment_version": "paired-target-v2-trimmed",
    "context_prompt_version": CONTEXT_PROMPT_VERSION,
    "context_texts": [ENGLISH_CONTEXT_INSTRUCTION],
    "contextual_resource_id": "seed-tts-2.0",
    "contextual_model": VOLCENGINE_TTS_MODEL,
    "contextual_voice": "saturn_zh_female_qingyingduoduo_cs_tob",
    "intro_speed_rate": 0,
    "word_speed_rate": -30,
    "segment_speed_rate": 0,
    "words_per_segment": MAX_INTROS,
    "output_format": "mp3",
    "voices": {
        "volcengine": "en_female_skye_emo_v2_mars_bigtts",
        "edge": "en-US-JennyNeural",
    },
}

CHINESE_CONFIG = {
    "active": "volcengine",
    "generation_mode": "fragments",
    "resource_id": "seed-tts-1.0",
    "model": "seed-tts-1.0",
    "fragment_version": "paired-target-v2-trimmed",
    "context_prompt_version": CONTEXT_PROMPT_VERSION,
    "context_texts": [CHINESE_CONTEXT_INSTRUCTION],
    "contextual_resource_id": "seed-tts-2.0",
    "contextual_model": VOLCENGINE_TTS_MODEL,
    "contextual_voice": "zh_female_yingyujiaoxue_uranus_bigtts",
    "intro_speed_rate": 0,
    "word_speed_rate": -30,
    "segment_speed_rate": 0,
    "words_per_segment": MAX_INTROS,
    "output_format": "mp3",
    "voices": {
        "volcengine": "zh_male_beijingxiaoye_emo_v2_mars_bigtts",
        "edge": "zh-CN-XiaoxiaoNeural",
    },
}

# ─── TTS Providers ───
def _tts_volcengine(
    text,
    output_path,
    voice,
    speech_rate=None,
    retry_requests=False,
    context_texts=None,
    resource_id=None,
    model=None,
):
    _load_env()

    import requests

    api_key = os.environ.get("VOLCENGINE_TTS_API_KEY", "")
    app_id = os.environ.get("VOLCENGINE_TTS_APP_ID", "")
    access_key = os.environ.get("VOLCENGINE_TTS_ACCESS_KEY", "")
    resource_id = resource_id or os.environ.get(
        "VOLCENGINE_TTS_RESOURCE_ID", "seed-tts-2.0"
    )

    use_api_key = bool(api_key)
    if not use_api_key and not (app_id and access_key):
        raise RuntimeError(
            "VOLCENGINE_TTS_API_KEY or "
            "(VOLCENGINE_TTS_APP_ID + VOLCENGINE_TTS_ACCESS_KEY) required"
        )

    text = str(text).strip()
    if not text:
        raise RuntimeError("empty input text")

    headers = {
        "Content-Type": "application/json",
        "X-Api-Resource-Id": resource_id,
        "X-Api-Request-Id": str(uuid.uuid4()),
    }
    if use_api_key:
        headers["X-Api-Key"] = api_key
        headers["X-Api-App-Key"] = "aGjiRDfUWi"
    else:
        headers["X-Api-App-Id"] = app_id
        headers["X-Api-Access-Key"] = access_key

    audio_params = {
        "format": "mp3",
        "sample_rate": 24000,
    }
    if speech_rate is not None:
        audio_params["speech_rate"] = int(speech_rate)

    req_params = {
        "text": text,
        "speaker": voice,
        "audio_params": audio_params,
    }
    if model:
        req_params["model"] = model
    if context_texts:
        req_params["additions"] = json.dumps(
            {"context_texts": [str(context_texts[0])]},
            ensure_ascii=False,
        )

    payload = {
        "user": {"uid": "hermes-tts"},
        "req_params": req_params,
    }

    max_attempts = 3 if retry_requests else 1
    for attempt in range(1, max_attempts + 1):
        try:
            response = requests.post(
                VOLCENGINE_TTS_URL,
                headers=headers,
                json=payload,
                stream=True,
                timeout=30,
            )
            response.raise_for_status()

            audio_chunks = []
            for line in response.iter_lines():
                if not line:
                    continue
                try:
                    chunk = json.loads(line)
                except json.JSONDecodeError:
                    continue

                code = chunk.get("code")
                if code is not None and code not in (0, 20000000):
                    raise RuntimeError(f"API error: code={code} msg={chunk.get('message', '')}")
                if chunk.get("data"):
                    audio_chunks.append(base64.b64decode(chunk["data"]))

            audio_data = b"".join(audio_chunks)
            if not audio_data:
                raise RuntimeError("no audio data received")

            output_dir = os.path.dirname(os.path.abspath(output_path))
            if output_dir:
                os.makedirs(output_dir, exist_ok=True)
            with open(output_path, "wb") as f:
                f.write(audio_data)
            return
        except (requests.exceptions.Timeout, requests.exceptions.ConnectionError) as exc:
            if os.path.exists(output_path):
                os.unlink(output_path)
            if attempt < max_attempts:
                print(f"[volcengine] request failed, retrying in 3s ({attempt}/{max_attempts}): {exc}", file=sys.stderr)
                time.sleep(3)
                continue
            raise RuntimeError(f"Volcengine TTS failed for {text!r}: {exc}") from exc
        except Exception as exc:
            if os.path.exists(output_path):
                os.unlink(output_path)
            raise RuntimeError(f"Volcengine TTS failed for {text!r}: {exc}") from exc


async def _tts_edge_async(text, output_path, voice, speech_rate=None):
    import edge_tts
    kwargs = {"voice": voice}
    if speech_rate is not None:
        kwargs["rate"] = f"{int(speech_rate):+d}%"
    c = edge_tts.Communicate(text, **kwargs)
    await c.save(output_path)


def _tts_edge(text, output_path, voice, speech_rate=None):
    asyncio.run(_tts_edge_async(text, output_path, voice, speech_rate))


PROVIDERS = {
    "volcengine": _tts_volcengine,
    "edge": _tts_edge,
}


def _provider_order(active):
    if active not in PROVIDERS:
        raise ValueError(f"active provider must be one of: {', '.join(PROVIDERS)}; got {active!r}")
    return [active] + [name for name in PROVIDERS if name != active]


def _tts_generate(
    text,
    output_path,
    active,
    voices,
    speech_rate=None,
    context_texts=None,
    resource_id=None,
    model=None,
):
    """Single TTS call with provider fallback."""
    errors = []
    for provider in _provider_order(active):
        if provider not in voices:
            errors.append(f"{provider}: voice not configured")
            continue
        try:
            kwargs = {"speech_rate": speech_rate}
            if provider == "volcengine":
                kwargs["retry_requests"] = provider == active
                kwargs["context_texts"] = context_texts
                kwargs["resource_id"] = resource_id
                kwargs["model"] = model
            PROVIDERS[provider](text, output_path, voices[provider], **kwargs)
            if provider != active:
                print(f"[fallback] used {provider} instead of {active}", file=sys.stderr)
            return provider
        except Exception as exc:
            errors.append(f"{provider}: {exc}")
            print(f"[{provider}] failed: {exc}", file=sys.stderr)
            if os.path.exists(output_path):
                os.unlink(output_path)
    raise RuntimeError("All TTS providers failed: " + " | ".join(errors))


# ─── Fragment Caching ───
def _chunk_words(words, size):
    return [words[i:i + size] for i in range(0, len(words), size)]


def _speech_rate_dir(root, speech_rate):
    return os.path.join(root, str(int(speech_rate)))


def _cache_model(config):
    if config["active"] == "volcengine":
        return config.get("model", VOLCENGINE_TTS_MODEL)
    return config["active"]


def _cache_metadata(config, speech_rate):
    active = config["active"]
    return {
        "provider": active,
        "voice": config["voices"][active],
        "model": _cache_model(config),
        "resource_id": config.get("resource_id", ""),
        "speech_rate": int(speech_rate),
        "fragment_version": config.get("fragment_version", ""),
    }


def _cache_meta_path(cache_dir):
    return os.path.join(cache_dir, ".cache_meta.json")


def _cache_metadata_matches(cache_dir, config, speech_rate):
    try:
        with open(_cache_meta_path(cache_dir), encoding="utf-8") as f:
            return json.load(f) == _cache_metadata(config, speech_rate)
    except (FileNotFoundError, json.JSONDecodeError, OSError):
        return False


def _write_cache_metadata(cache_dir, config, speech_rate):
    tmp_path = _cache_meta_path(cache_dir) + f".tmp.{os.getpid()}"
    with open(tmp_path, "w", encoding="utf-8") as f:
        json.dump(_cache_metadata(config, speech_rate), f, ensure_ascii=False, indent=2)
    os.replace(tmp_path, _cache_meta_path(cache_dir))


def _prepare_cache_dir(cache_dir, config, speech_rate):
    os.makedirs(cache_dir, exist_ok=True)
    if _cache_metadata_matches(cache_dir, config, speech_rate):
        return
    for filename in os.listdir(cache_dir):
        path = os.path.join(cache_dir, filename)
        if os.path.isfile(path) and filename != ".cache_meta.json":
            os.remove(path)
    _write_cache_metadata(cache_dir, config, speech_rate)


def _cache_dirs(config):
    active = config["active"]
    voice_id = config["voices"][active]
    root = os.path.join(CACHE_DIR, voice_id)
    intros_root = os.path.join(root, "intros")
    words_root = os.path.join(root, "words")
    dirs = {
        "root": root,
        "intros": _speech_rate_dir(intros_root, config["intro_speed_rate"]),
        "words": _speech_rate_dir(words_root, config["word_speed_rate"]),
        "silence": os.path.join(root, "silence"),
        "segments": os.path.join(root, "segments"),
    }
    for path in dirs.values():
        os.makedirs(path, exist_ok=True)
    _prepare_cache_dir(dirs["intros"], config, config["intro_speed_rate"])
    _prepare_cache_dir(dirs["words"], config, config["word_speed_rate"])
    return dirs


def _file_ready(path):
    return os.path.exists(path) and os.path.getsize(path) > 0


def _tmp_audio_path(output_path):
    root, ext = os.path.splitext(output_path)
    return f"{root}.tmp.{os.getpid()}{ext or '.mp3'}"


def _trim_audio_edges(path):
    """Remove only leading/trailing silence while preserving internal pauses."""
    trimmed_path = _tmp_audio_path(path) + ".trim.mp3"
    if os.path.exists(trimmed_path):
        os.unlink(trimmed_path)
    cmd = [
        "ffmpeg",
        "-y",
        "-hide_banner",
        "-loglevel", "error",
        "-i", path,
        "-af",
        (
            "silenceremove="
            "start_periods=1:start_silence=0.05:start_threshold=-45dB:"
            "stop_periods=1:stop_silence=2.0:stop_threshold=-45dB"
        ),
        "-ar", "24000",
        "-ac", "1",
        "-acodec", "libmp3lame",
        "-q:a", "4",
        trimmed_path,
    ]
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        if result.returncode != 0 or not _file_ready(trimmed_path):
            raise RuntimeError(f"ffmpeg silence trim failed: {result.stderr}")
        os.replace(trimmed_path, path)
    finally:
        if os.path.exists(trimmed_path):
            os.unlink(trimmed_path)


def _fallback_audio_path(dirs, date_compact, language, kind, label, output_format, config, speech_rate):
    fallback_provider = next((name for name in PROVIDERS if name != config["active"]), "")
    digest_payload = {
        "label": str(label),
        "provider": fallback_provider,
        "voice": config["voices"].get(fallback_provider, ""),
        "model": fallback_provider,
        "speech_rate": int(speech_rate),
    }
    digest = hashlib.sha1(
        json.dumps(digest_payload, ensure_ascii=False, sort_keys=True).encode("utf-8")
    ).hexdigest()[:12]
    filename = f"_fallback_{date_compact}_{language}_{kind}_{digest}.{output_format}"
    return os.path.join(dirs["segments"], filename)


def _generate_tts_fragment(text, output_path, config, speech_rate, fallback_output_path=None):
    if _file_ready(output_path) and _cache_metadata_matches(os.path.dirname(output_path), config, speech_rate):
        return False, None
    if fallback_output_path and _file_ready(fallback_output_path):
        return False, "fallback-cache"

    os.makedirs(os.path.dirname(output_path), exist_ok=True)
    tmp_base = fallback_output_path or output_path
    tmp_path = _tmp_audio_path(tmp_base)
    if os.path.exists(tmp_path):
        os.unlink(tmp_path)
    try:
        actual_provider = _tts_generate(
            text,
            tmp_path,
            active=config["active"],
            voices=config["voices"],
            speech_rate=speech_rate,
            resource_id=config.get("resource_id"),
            model=config.get("model"),
        )
        if not _file_ready(tmp_path):
            raise RuntimeError(f"TTS produced empty file: {tmp_path}")
        _trim_audio_edges(tmp_path)
        if actual_provider == config["active"]:
            os.replace(tmp_path, output_path)
            _write_cache_metadata(os.path.dirname(output_path), config, speech_rate)
        else:
            if not fallback_output_path:
                raise RuntimeError("fallback output path required for non-active provider")
            os.makedirs(os.path.dirname(fallback_output_path), exist_ok=True)
            os.replace(tmp_path, fallback_output_path)
        return True, actual_provider
    finally:
        if os.path.exists(tmp_path):
            os.unlink(tmp_path)


def _fragment_name(text):
    name = str(text).strip().replace("\x00", "")
    name = name.replace("/", "／")
    if os.altsep:
        name = name.replace(os.altsep, "／")
    if name in ("", ".", ".."):
        raise ValueError(f"Invalid fragment filename for {text!r}")
    return name


def _english_intro_text(idx):
    if idx > len(NUMBER_WORDS):
        raise ValueError(f"English intro supports up to {len(NUMBER_WORDS)} items; got {idx}")
    return f"Number {NUMBER_WORDS[idx - 1]}."


def _chinese_number(idx):
    digits = ["", "一", "二", "三", "四", "五", "六", "七", "八", "九"]
    if 1 <= idx <= 9:
        return digits[idx]
    if idx == 10:
        return "十"
    if 11 <= idx <= 19:
        return "十" + digits[idx - 10]
    if idx in (20, 30):
        return digits[idx // 10] + "十"
    if 21 <= idx <= 29:
        return "二十" + digits[idx - 20]
    raise ValueError(f"Chinese intro supports 1-{MAX_INTROS}; got {idx}")


def _chinese_intro_text(idx):
    return f"第{_chinese_number(idx)}个词。"


def _intro_text(language, idx):
    if language == "english":
        return _english_intro_text(idx)
    if language == "chinese":
        return _chinese_intro_text(idx)
    raise ValueError(f"Unknown language: {language}")


def _scene_text(language, kind):
    texts = {
        "english": {
            "opening": "Let's begin the spelling dictation. Listen carefully.",
            "continue": "Let's continue the spelling dictation.",
            "closing": "This part is complete.",
        },
        "chinese": {
            "opening": "现在开始词语默写，请仔细听。",
            "continue": "继续词语默写。",
            "closing": "本段听写结束。",
        },
    }
    try:
        return texts[language][kind]
    except KeyError as exc:
        raise ValueError(
            f"Unknown scene text: language={language!r} kind={kind!r}"
        ) from exc


def _contextual_segment_text(words, language, start_idx, is_first_segment):
    """Build one spoken classroom scene for a complete TTS segment."""
    if language == "english":
        opening = (
            "Let's begin the spelling dictation. Listen carefully."
            if is_first_segment
            else "Let's continue the spelling dictation."
        )
        items = []
        for offset, word in enumerate(words):
            idx = start_idx + offset
            spoken = _tts_input_text(word, language).rstrip(".!?")
            items.append(f"Number {NUMBER_WORDS[idx - 1]}. {spoken}. {spoken}...")
        return "\n\n".join([opening, *items, "This part is complete."])

    if language == "chinese":
        opening = "现在开始词语听写，请仔细听。" if is_first_segment else "继续词语听写。"
        items = []
        for offset, word in enumerate(words):
            idx = start_idx + offset
            items.append(f"第{_chinese_number(idx)}个词。{word}。{word}。……")
        return "\n\n".join([opening, *items, "本段听写结束。"])

    raise ValueError(f"Unknown language: {language}")


def _ensure_intros_cached(config, dirs, language, date_compact, count):
    paths = {}
    generated = 0
    for idx in range(1, count + 1):
        path = os.path.join(dirs["intros"], f"number_{idx}.{config['output_format']}")
        fallback_path = _fallback_audio_path(
            dirs,
            date_compact,
            language,
            "intro",
            idx,
            config["output_format"],
            config,
            config["intro_speed_rate"],
        )
        was_generated, actual_provider = _generate_tts_fragment(
            _intro_text(language, idx),
            path,
            config,
            config["intro_speed_rate"],
            fallback_path,
        )
        if was_generated:
            generated += 1
        paths[idx] = fallback_path if actual_provider and actual_provider != config["active"] else path
    if generated:
        print(f"  Intros: generated {generated}, cached {count - generated}", file=sys.stderr)
    else:
        print(f"  Intros: all {count} cached", file=sys.stderr)
    return paths


def _ensure_scene_cached(config, dirs, language, date_compact):
    paths = {}
    for kind in ("opening", "continue", "closing"):
        path = os.path.join(
            dirs["intros"], f"scene_{language}_{kind}.{config['output_format']}"
        )
        fallback_path = _fallback_audio_path(
            dirs,
            date_compact,
            language,
            "scene",
            kind,
            config["output_format"],
            config,
            config["intro_speed_rate"],
        )
        _, actual_provider = _generate_tts_fragment(
            _scene_text(language, kind),
            path,
            config,
            config["intro_speed_rate"],
            fallback_path,
        )
        paths[kind] = (
            fallback_path
            if actual_provider and actual_provider != config["active"]
            else path
        )
    return paths


def _word_audio_path(dirs, word, output_format):
    return os.path.join(dirs["words"], f"{_fragment_name(word)}.{output_format}")


def _tts_input_text(text, language):
    """Return text optimized for TTS while keeping the original word as cache key."""
    text = str(text).strip()
    # Volcengine's English voice may spell bare multi-word phrases letter by letter
    # (e.g. "go away" -> "go, a, w, a, y"). A sentence-final punctuation mark
    # nudges it to read the phrase normally without changing the dictation answer.
    if language == "english" and any(ch.isspace() for ch in text) and text[-1:] not in ".!?":
        return f"{text}."
    return text


def _paired_word_text(word, language):
    spoken = _tts_input_text(word, language).rstrip(".!?。！？")
    if language == "english":
        return f"{spoken}. {spoken}."
    if language == "chinese":
        return f"{spoken}。{spoken}。"
    raise ValueError(f"Unknown language: {language}")


def _ensure_words_cached(words, config, dirs, language, date_compact):
    paths = {}
    generated = 0
    seen = set()
    for word in words:
        if word in seen:
            continue
        seen.add(word)
        path = _word_audio_path(dirs, word, config["output_format"])
        fallback_path = _fallback_audio_path(
            dirs,
            date_compact,
            language,
            "word",
            word,
            config["output_format"],
            config,
            config["word_speed_rate"],
        )
        was_generated, actual_provider = _generate_tts_fragment(
            _paired_word_text(word, language),
            path,
            config,
            config["word_speed_rate"],
            fallback_path,
        )
        if was_generated:
            generated += 1
        paths[word] = fallback_path if actual_provider and actual_provider != config["active"] else path

    cached = len(seen) - generated
    print(f"  Paired words: generated {generated}, cached {cached}", file=sys.stderr)
    return paths


def _silence_path(dirs, duration):
    ms = int(round(duration * 1000))
    return ms, os.path.join(dirs["silence"], f"silence_{ms}ms.mp3")


def _ensure_silence_files(dirs):
    paths = {}
    generated = 0
    for duration in (SILENCE_AFTER_INTRO, SILENCE_BETWEEN_REPEATS, SILENCE_AFTER_WORD):
        ms, path = _silence_path(dirs, duration)
        if _file_ready(path):
            paths[ms] = path
            continue

        os.makedirs(os.path.dirname(path), exist_ok=True)
        tmp_path = _tmp_audio_path(path)
        if os.path.exists(tmp_path):
            os.unlink(tmp_path)
        cmd = [
            "ffmpeg",
            "-y",
            "-f", "lavfi",
            "-i", "anullsrc=r=24000:cl=mono",
            "-t", str(duration),
            "-acodec", "libmp3lame",
            "-q:a", "9",
            tmp_path,
        ]
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        if result.returncode != 0:
            if os.path.exists(tmp_path):
                os.unlink(tmp_path)
            raise RuntimeError(f"ffmpeg silence generation failed for {duration}s: {result.stderr}")
        if not _file_ready(tmp_path):
            raise RuntimeError(f"ffmpeg produced empty silence file: {tmp_path}")
        os.replace(tmp_path, path)
        paths[ms] = path
        generated += 1

    if generated:
        print(f"  Silence: generated {generated}, cached {len(paths) - generated}", file=sys.stderr)
    else:
        print(f"  Silence: all {len(paths)} cached", file=sys.stderr)
    return paths


def _concat_fragments(fragment_paths, output_path):
    missing = [path for path in fragment_paths if not _file_ready(path)]
    if missing:
        raise RuntimeError(f"Cannot concatenate missing/empty fragments: {missing[:3]}")

    os.makedirs(os.path.dirname(output_path), exist_ok=True)
    tmp_output = _tmp_audio_path(output_path)
    if os.path.exists(tmp_output):
        os.unlink(tmp_output)

    try:
        with tempfile.TemporaryDirectory() as tmp_dir:
            concat_path = os.path.join(tmp_dir, "concat.txt")
            with open(concat_path, "w", encoding="utf-8") as f:
                for idx, path in enumerate(fragment_paths, start=1):
                    link_path = os.path.join(tmp_dir, f"part_{idx:04d}.mp3")
                    try:
                        os.symlink(os.path.abspath(path), link_path)
                    except OSError:
                        # Windows: symlink requires admin/Developer Mode (WinError 1314);
                        # fall back to copy so ffmpeg concat works everywhere.
                        shutil.copy2(path, link_path)
                    f.write(f"file '{link_path}'\n")

            cmd = [
                "ffmpeg",
                "-y",
                "-hide_banner",
                "-loglevel", "error",
                "-f", "concat",
                "-safe", "0",
                "-i", concat_path,
                "-vn",
                "-ar", "24000",
                "-ac", "1",
                "-acodec", "libmp3lame",
                "-q:a", "4",
                tmp_output,
            ]
            result = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
            if result.returncode != 0:
                raise RuntimeError(f"ffmpeg concat failed: {result.stderr}")

        if not _file_ready(tmp_output):
            raise RuntimeError(f"ffmpeg produced empty segment: {tmp_output}")
        os.replace(tmp_output, output_path)
    finally:
        if os.path.exists(tmp_output):
            os.unlink(tmp_output)


# ─── Main Audio Generation ───
def _contextual_segment_path(dirs, prefix, date_compact, batch_index, text, config):
    digest_payload = {
        "text": text,
        "provider": config["active"],
        "voice": config["voices"][config["active"]],
        "model": _cache_model(config),
        "speech_rate": int(config.get("segment_speed_rate", 0)),
        "prompt_version": config.get("context_prompt_version", CONTEXT_PROMPT_VERSION),
        "context_texts": config.get("context_texts", []),
    }
    digest = hashlib.sha1(
        json.dumps(digest_payload, ensure_ascii=False, sort_keys=True).encode("utf-8")
    ).hexdigest()[:12]
    filename = f"{prefix}_{date_compact}_{batch_index}_{digest}.{config['output_format']}"
    return os.path.join(dirs["segments"], filename)


def _generate_contextual_audio(words, config, prefix, language, date_compact, dirs):
    output_paths = []
    batches = _chunk_words(words, config["words_per_segment"])
    for batch_index, batch in enumerate(batches, start=1):
        start_idx = (batch_index - 1) * config["words_per_segment"] + 1
        text = _contextual_segment_text(
            batch,
            language,
            start_idx,
            is_first_segment=batch_index == 1,
        )
        output_path = _contextual_segment_path(
            dirs, prefix, date_compact, batch_index, text, config
        )
        if _file_ready(output_path):
            print(f"  Segment {batch_index}: contextual cache hit", file=sys.stderr)
            output_paths.append(output_path)
            continue

        tmp_path = _tmp_audio_path(output_path)
        if os.path.exists(tmp_path):
            os.unlink(tmp_path)
        try:
            actual_provider = _tts_generate(
                text,
                tmp_path,
                active=config["active"],
                voices=config["voices"],
                speech_rate=config.get("segment_speed_rate", 0),
                context_texts=config.get("context_texts"),
                resource_id=config.get("resource_id"),
                model=config.get("model"),
            )
            if not _file_ready(tmp_path):
                raise RuntimeError(f"TTS produced empty file: {tmp_path}")
            os.replace(tmp_path, output_path)
            print(
                f"  Segment {batch_index}: generated {len(batch)} words "
                f"in one {actual_provider} request",
                file=sys.stderr,
            )
        finally:
            if os.path.exists(tmp_path):
                os.unlink(tmp_path)
        output_paths.append(output_path)
    return output_paths


def _generate_fragment_audio(words, config, prefix, language, date_compact, dirs):
    """Generate normal-speed prompts and slow paired targets, then concatenate."""
    intro_paths = _ensure_intros_cached(config, dirs, language, date_compact, len(words))
    scene_paths = _ensure_scene_cached(config, dirs, language, date_compact)
    word_paths = _ensure_words_cached(words, config, dirs, language, date_compact)
    silence_paths = _ensure_silence_files(dirs)

    after_intro_ms = int(round(SILENCE_AFTER_INTRO * 1000))
    after_word_ms = int(round(SILENCE_AFTER_WORD * 1000))
    output_paths = []

    for batch_index, batch in enumerate(_chunk_words(words, config["words_per_segment"]), start=1):
        filename = f"{prefix}_{date_compact}_{batch_index}.{config['output_format']}"
        output_path = os.path.join(dirs["segments"], filename)
        start_idx = (batch_index - 1) * config["words_per_segment"] + 1

        fragment_paths = [
            scene_paths["opening" if batch_index == 1 else "continue"],
            silence_paths[after_intro_ms],
        ]
        for offset, word in enumerate(batch):
            word_idx = start_idx + offset
            fragment_paths.extend([
                intro_paths[word_idx],
                silence_paths[after_intro_ms],
                word_paths[word],
                silence_paths[after_word_ms],
            ])
        fragment_paths.append(scene_paths["closing"])

        print(f"  Segment {batch_index}: {len(batch)} words, {len(fragment_paths)} fragments", file=sys.stderr)
        _concat_fragments(fragment_paths, output_path)
        output_paths.append(output_path)
    return output_paths


def _generate_bare_audio(words, config, prefix, language, date_compact, dirs):
    """Emit one bare-word track per word (with trailing silence), in input order.

    Each track is the bare TTS for a single word followed by
    SILENCE_AFTER_WORD seconds of silence. No scene/intro narration is added.
    The trailing silence preserves the word's natural phoneme decay/reverb so
    it does not sound truncated. Used for per-word review/practice where
    dictation narration is undesirable. Returns paths in input order so
    callers can map paths[i] <-> words[i] positionally.
    """
    word_paths = _ensure_words_cached(words, config, dirs, language, date_compact)
    silence_paths = _ensure_silence_files(dirs)
    after_word_ms = int(round(SILENCE_AFTER_WORD * 1000))
    output_paths = []

    for idx, word in enumerate(words, start=1):
        filename = f"{prefix}_{date_compact}_{idx}.{config['output_format']}"
        output_path = os.path.join(dirs["segments"], filename)
        if _file_ready(output_path):
            output_paths.append(output_path)
            continue
        fragment_paths = [
            word_paths[word],
            silence_paths[after_word_ms],
        ]
        print(f"  Bare word {idx}: '{word}' + {after_word_ms}ms silence", file=sys.stderr)
        _concat_fragments(fragment_paths, output_path)
        output_paths.append(output_path)
    return output_paths


def _config_for_mode(config, mode):
    effective = dict(config)
    effective["voices"] = dict(config["voices"])
    if mode == "contextual-segment":
        effective["resource_id"] = config.get("contextual_resource_id", "seed-tts-2.0")
        effective["model"] = config.get("contextual_model", VOLCENGINE_TTS_MODEL)
        if config.get("contextual_voice"):
            effective["voices"][effective["active"]] = config["contextual_voice"]
    return effective


def generate_dictation_audio(words, config, prefix, language, date_compact=None):
    """Generate TTS 1.0 fragments by default, or a contextual TTS 2.0 segment."""
    words = [str(w).strip() for w in words if w is not None and str(w).strip()]
    if not words:
        return []
    if not _prefix_is_safe(prefix):
        raise ValueError("prefix contains unsafe characters")
    if len(words) > MAX_INTROS:
        raise ValueError(f"Dictation generation supports up to {MAX_INTROS} words; got {len(words)}")

    if date_compact is None:
        cst = ZoneInfo("Asia/Shanghai")
        date_compact = datetime.datetime.now(cst).date().isoformat().replace("-", "")

    mode = config.get("generation_mode", "fragments")
    config = _config_for_mode(config, mode)
    dirs = _cache_dirs(config)
    if mode == "contextual-segment":
        return _generate_contextual_audio(words, config, prefix, language, date_compact, dirs)
    if mode == "fragments":
        return _generate_fragment_audio(words, config, prefix, language, date_compact, dirs)
    if mode == "bare":
        return _generate_bare_audio(words, config, prefix, language, date_compact, dirs)
    raise ValueError(f"Unknown generation mode: {mode!r}")


def fetch_due_words(category, limit=30):
    """Fetch and atomically claim due words from vocab_manager."""
    cmd = [
        PYTHON, VOCAB_MANAGER, "due",
        "--limit", str(limit),
        "--category", category,
    ]
    result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
    if result.returncode != 0:
        print(f"WARNING: vocab_manager due failed for {category}: {result.stderr}", file=sys.stderr)
        return []
    try:
        words = json.loads(result.stdout)
        words = words if isinstance(words, list) else []
    except json.JSONDecodeError:
        print(f"WARNING: could not parse vocab_manager output for {category}", file=sys.stderr)
        return []
    return words


# ─── Cleanup Old Caches ───
def cleanup_old_caches(max_age_days=2):
    """Remove old daily manifests and segment files; keep fragment caches."""
    cst = ZoneInfo("Asia/Shanghai")
    today_cst = datetime.datetime.now(cst).date()
    cutoff_date = (today_cst - datetime.timedelta(days=max_age_days)).isoformat()
    cutoff_compact = cutoff_date.replace("-", "")
    removed = 0

    if os.path.exists(MANIFEST_DIR):
        for f in os.listdir(MANIFEST_DIR):
            if f.startswith("dictation_cache_") and f.endswith(".json"):
                date_str = f.replace("dictation_cache_", "").replace(".json", "")
                if date_str < cutoff_date:
                    os.remove(os.path.join(MANIFEST_DIR, f))
                    removed += 1

    if os.path.exists(CACHE_DIR):
        for voice_id in os.listdir(CACHE_DIR):
            segments_dir = os.path.join(CACHE_DIR, voice_id, "segments")
            if not os.path.isdir(segments_dir):
                continue
            for f in os.listdir(segments_dir):
                if not (f.endswith(".ogg") or f.endswith(".mp3")):
                    continue
                date_parts = [part for part in f.split("_") if len(part) == 8 and part.isdigit()]
                if date_parts and date_parts[0] < cutoff_compact:
                    os.remove(os.path.join(segments_dir, f))
                    removed += 1

    return removed


# ─── Main ───
def _preflight_check():
    try:
        os.makedirs(CACHE_DIR, exist_ok=True)
        os.makedirs(MANIFEST_DIR, exist_ok=True)
    except OSError as e:
        raise RuntimeError(f"cache directory setup failed: {e}") from e

    if not os.access(CACHE_DIR, os.W_OK):
        raise RuntimeError(f"CACHE_DIR is not writable: {CACHE_DIR}")

    if not shutil.which("ffmpeg"):
        raise RuntimeError("ffmpeg is not executable or not found in PATH")

    usage = shutil.disk_usage(CACHE_DIR)
    free_mb = usage.free / 1024 / 1024
    if free_mb <= 100:
        raise RuntimeError(f"disk free space too low: {free_mb:.1f}MB available")


def _positive_int(value):
    try:
        parsed = int(value)
    except ValueError as e:
        raise argparse.ArgumentTypeError("must be an integer") from e
    if parsed < 1:
        raise argparse.ArgumentTypeError("must be >= 1")
    return parsed


def _prefix_is_safe(prefix):
    return isinstance(prefix, str) and all(
        ch.isascii() and (ch.isalnum() or ch in "_.-")
        for ch in prefix
    )


def _parse_generate_args():
    parser = argparse.ArgumentParser(
        prog="pregenerate_dictation.py generate",
        description="Generate dictation audio from a word list.",
    )
    parser.add_argument("--words", required=True, help="JSON array of words, e.g. '[\"apple\",\"banana\"]'")
    parser.add_argument("--language", required=True, choices=["english", "chinese"],
                        help="Dictation language")
    parser.add_argument("--words-per-segment", type=_positive_int, default=None,
                        help="Words per audio segment (default: all requested words, up to 30)")
    parser.add_argument("--prefix", default=None,
                        help="Output filename prefix (default: eng_ or chn_ + timestamp)")
    parser.add_argument("--voice", default=None,
                        help="Override voice ID (optional)")
    parser.add_argument(
        "--generation-mode",
        choices=["contextual-segment", "fragments", "bare"],
        default=None,
        help="TTS strategy (default: TTS 1.0 fragments; contextual-segment uses TTS 2.0; bare emits one bare-word track per word with no narration)",
    )
    return parser.parse_args()


def _cli_generate(args=None):
    """CLI entry point for generating dictation audio from explicit word list."""
    if args is None:
        args = _parse_generate_args()

    try:
        words = json.loads(args.words)
    except json.JSONDecodeError as e:
        print(f"ERROR: invalid --words JSON: {e}", file=sys.stderr)
        sys.exit(1)

    if not isinstance(words, list) or not words:
        print("ERROR: --words must be a non-empty JSON array", file=sys.stderr)
        sys.exit(1)

    parsed_words = []
    for index, w in enumerate(words, start=1):
        if isinstance(w, dict):
            word_val = w.get("word")
            if "word" not in w or not isinstance(word_val, str) or not word_val.strip():
                print(f"ERROR: item {index} missing or invalid 'word' field", file=sys.stderr)
                sys.exit(1)
            parsed_words.append(word_val.strip())
        elif isinstance(w, str):
            s = w.strip()
            if s:
                parsed_words.append(s)
        else:
            print(f"ERROR: --words items must be strings or {{'word': '...'}} objects, got {type(w).__name__}", file=sys.stderr)
            sys.exit(1)

    if not parsed_words:
        print("ERROR: all words are empty after stripping", file=sys.stderr)
        sys.exit(1)

    if len(parsed_words) > MAX_INTROS:
        print(f"ERROR: dictation generation supports up to {MAX_INTROS} words; got {len(parsed_words)}", file=sys.stderr)
        sys.exit(1)

    if args.prefix is not None and not _prefix_is_safe(args.prefix):
        print("ERROR: --prefix contains unsafe characters", file=sys.stderr)
        sys.exit(1)

    try:
        _preflight_check()
    except Exception as e:
        print(f"ERROR: preflight failed: {e}", file=sys.stderr)
        sys.exit(1)

    if args.language == "english":
        config = dict(ENGLISH_CONFIG)
        default_prefix = "eng"
    else:
        config = dict(CHINESE_CONFIG)
        default_prefix = "chn"

    if args.words_per_segment is not None:
        config["words_per_segment"] = args.words_per_segment
    if args.generation_mode is not None:
        config["generation_mode"] = args.generation_mode

    if args.voice:
        config["voices"] = dict(config["voices"])
        config["voices"][config["active"]] = args.voice
        config["contextual_voice"] = args.voice

    cst = ZoneInfo("Asia/Shanghai")
    date_compact = datetime.datetime.now(cst).date().isoformat().replace("-", "")

    if args.prefix is not None:
        prefix = args.prefix
    else:
        ts = datetime.datetime.now(cst).strftime("%H%M%S%f")
        prefix = f"{default_prefix}_{ts}"

    if not _prefix_is_safe(prefix):
        print("ERROR: --prefix contains unsafe characters", file=sys.stderr)
        sys.exit(1)

    try:
        audio_files = generate_dictation_audio(parsed_words, config, prefix, args.language, date_compact)
    except Exception as e:
        print(f"ERROR: audio generation failed: {e}", file=sys.stderr)
        sys.exit(1)

    # Print absolute paths one per line for easy parsing
    for p in audio_files:
        print(os.path.abspath(p))


def _manifest_section_ready(section):
    if not section:
        return False
    audio_files = section.get("audio_files") or []
    return bool(audio_files) and all(_file_ready(path) for path in audio_files)


def _manifest_status(manifest):
    if manifest["errors"]:
        if manifest["english"] or manifest["chinese"]:
            return "partial"
        return "failed"
    return "success"


def _run_main():
    cst = ZoneInfo("Asia/Shanghai")
    now_cst = datetime.datetime.now(cst)
    today = now_cst.date().isoformat()
    today_compact = today.replace("-", "")
    manifest_path = os.path.join(MANIFEST_DIR, f"dictation_cache_{today}.json")

    try:
        _preflight_check()
    except Exception as e:
        print(f"ERROR: preflight failed: {e}", file=sys.stderr)
        sys.exit(1)

    existing = None

    # Check existing manifest
    if os.path.exists(manifest_path):
        try:
            with open(manifest_path, encoding="utf-8") as f:
                existing = json.load(f)
            if existing.get("date") == today and existing.get("status") == "success":
                print(f"Valid cache already exists for {today}, skipping.", file=sys.stderr)
                return
            elif existing.get("date") == today and existing.get("status") == "partial":
                print(f"Partial manifest found for {today}, filling missing sections.", file=sys.stderr)
            else:
                print(f"Stale/invalid manifest found for {today}, regenerating.", file=sys.stderr)
                existing = None
        except (json.JSONDecodeError, ValueError):
            print(f"Corrupt manifest found for {today}, regenerating.", file=sys.stderr)
            existing = None

    manifest = {
        "date": today,
        "generated_at": now_cst.isoformat(),
        "status": "failed",
        "errors": [],
        "english": existing.get("english") if existing else None,
        "chinese": existing.get("chinese") if existing else None,
    }

    errors = []

    # ── English ──
    if _manifest_section_ready(manifest["english"]):
        print("English: existing partial cache is ready, skipped", file=sys.stderr)
    else:
        manifest["english"] = None
        try:
            eng_words_raw = fetch_due_words("english", 30)
            if eng_words_raw:
                eng_words = [{"id": w["id"], "word": w["word"], "meaning": w["meaning"]} for w in eng_words_raw]
                print(f"English: generating {len(eng_words)} words...", file=sys.stderr)
                eng_audio = generate_dictation_audio(
                    [w["word"] for w in eng_words],
                    ENGLISH_CONFIG,
                    "eng",
                    "english",
                    today_compact,
                )
                manifest["english"] = {"words": eng_words, "audio_files": eng_audio}
                print(f"English: {len(eng_words)} words, {len(eng_audio)} audio segments", file=sys.stderr)
            else:
                print("English: no due words, skipped", file=sys.stderr)
        except Exception as e:
            errors.append(f"English: {e}")
            print(f"English: ERROR - {e} (claimed words will expire automatically)", file=sys.stderr)

    # ── Chinese ──
    if _manifest_section_ready(manifest["chinese"]):
        print("Chinese: existing partial cache is ready, skipped", file=sys.stderr)
    else:
        manifest["chinese"] = None
        try:
            chn_words_raw = fetch_due_words("chinese", 30)
            if chn_words_raw:
                chn_words = [{"id": w["id"], "word": w["word"], "meaning": w["meaning"]} for w in chn_words_raw]
                print(f"Chinese: generating {len(chn_words)} words...", file=sys.stderr)
                chn_audio = generate_dictation_audio(
                    [w["word"] for w in chn_words],
                    CHINESE_CONFIG,
                    "chn",
                    "chinese",
                    today_compact,
                )
                manifest["chinese"] = {"words": chn_words, "audio_files": chn_audio}
                print(f"Chinese: {len(chn_words)} words, {len(chn_audio)} audio segments", file=sys.stderr)
            else:
                print("Chinese: no due words, skipped", file=sys.stderr)
        except Exception as e:
            errors.append(f"Chinese: {e}")
            print(f"Chinese: ERROR - {e} (claimed words will expire automatically)", file=sys.stderr)

    manifest["errors"] = errors
    manifest["status"] = _manifest_status(manifest)

    # ── Save manifest ──
    if manifest["english"] or manifest["chinese"] or errors:
        tmp_manifest = manifest_path + ".tmp"
        try:
            with open(tmp_manifest, "w", encoding="utf-8") as f:
                json.dump(manifest, f, ensure_ascii=False, indent=2)
            os.replace(tmp_manifest, manifest_path)
            print(f"Manifest: {manifest_path}", file=sys.stderr)
        except Exception as e:
            print(f"ERROR: failed to write manifest: {e}", file=sys.stderr)
            errors.append(f"Manifest: {e}")
            if os.path.exists(tmp_manifest):
                os.unlink(tmp_manifest)
    else:
        print("No audio generated, manifest not saved.", file=sys.stderr)

    # ── Cleanup ──
    try:
        removed = cleanup_old_caches()
        if removed:
            print(f"Cleanup: removed {removed} old cache files", file=sys.stderr)
    except Exception as e:
        print(f"Cleanup error (non-fatal): {e}", file=sys.stderr)

    # ── Summary ──
    if errors:
        status = "PARTIAL" if manifest["english"] or manifest["chinese"] else "FAILED"
        print(f"\nSTATUS: {status} ({len(errors)} errors)", file=sys.stderr)
        sys.exit(1)
    elif manifest["english"] or manifest["chinese"]:
        print("\nSTATUS: SUCCESS", file=sys.stderr)
    else:
        print("\nSTATUS: NO_WORDS", file=sys.stderr)


def _acquire_global_lock():
    # Cross-platform lock path: tempfile.gettempdir() returns /tmp on Linux
    # (byte-identical to the previous hardcoded /tmp/...) and %TEMP% on Windows.
    lock_path = os.path.join(tempfile.gettempdir(), "pregenerate_dictation.lock")
    lock_file = None
    try:
        lock_file = open(lock_path, "w", encoding="utf-8")
        if fcntl is None:
            # Windows/macOS dev host: no fcntl — dev inner loop only, no real
            # concurrency; production Linux path keeps the real flock below.
            pass
        else:
            # Linux production: real flock — byte-identical to pre-fix behavior.
            fcntl.flock(lock_file, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        print(f"Another pregenerate_dictation.py instance is running ({lock_path}); exiting.", file=sys.stderr)
        if lock_file is not None:
            lock_file.close()
        sys.exit(0)
    except OSError as e:
        print(f"ERROR: cannot acquire lock: {e}", file=sys.stderr)
        if lock_file is not None:
            lock_file.close()
        sys.exit(1)
    return lock_file


def _release_global_lock(lock_file):
    """Release the global pregenerate lock and close the lock file.

    On Linux (fcntl present) this issues LOCK_UN before close — byte-identical
    to the pre-fix call sites. On Windows/macOS dev host (fcntl is None) it
    only closes the file (the open/close pair is a harmless no-op lock).
    """
    if fcntl is not None:
        fcntl.flock(lock_file, fcntl.LOCK_UN)
    lock_file.close()


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "generate":
        sys.argv.pop(1)
        args = _parse_generate_args()
        lock_file = _acquire_global_lock()
        try:
            _cli_generate(args)
        finally:
            _release_global_lock(lock_file)
        return

    lock_file = _acquire_global_lock()
    try:
        if "--run-legacy-pregenerate" not in sys.argv:
            print(
                "Usage: pregenerate_dictation.py generate --words '[\"a\",\"b\"]' --language chinese\n"
                "       pregenerate_dictation.py --run-legacy-pregenerate  (maintenance only)",
                file=sys.stderr,
            )
            sys.exit(2)
        sys.argv.remove("--run-legacy-pregenerate")
        try:
            _run_main()
        finally:
            # Linux: LOCK_UN via helper (byte-identical to pre-fix); Windows: close-only no-op.
            if fcntl is not None:
                fcntl.flock(lock_file, fcntl.LOCK_UN)
    finally:
        lock_file.close()


if __name__ == "__main__":
    main()
