import { existsSync } from "node:fs";
import optchat from __OPTCHAT_INDEX__;
import { createProfile, loadConfig, profilePath, saveConfig } from __OPTCHAT_PROFILES__;

export default function sharedOptchatOwner(pi) {
  const profile = __OPTCHAT_PROFILE__;
  pi.on("session_start", (_event, ctx) => {
    const directory = profilePath(profile);
    if (!existsSync(directory)) {
      createProfile(profile);
      if (ctx.model) {
        const config = loadConfig(directory);
        const model = { provider: ctx.model.provider, model: ctx.model.id, thinking: pi.getThinkingLevel() };
        saveConfig(directory, { ...config, compactor: model, subagent: model, maxAgents: 32 });
      }
    }
  });
  optchat(pi);
}
