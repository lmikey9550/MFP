# Changelog

## 2026-05-16

- Added dashboard auto-refresh controls for 5s, 15s, 30s, 60s, or off.
- Added OpenAI-compatible client model discovery endpoints at `GET /v1/models`, `GET /models`, `GET /v1/models/{id}`, and `GET /models/{id}`.

## 2026-05-15

- Fixed frontend model candidate dropdowns resetting saved backend models to the first option.
- Preserved unknown provider/model values in candidate rows instead of silently replacing them during UI rendering.
- Added a Provider-based bulk candidate picker for adding multiple backend models to a frontend model at once.
