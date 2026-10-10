import { appendFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import semanticRelease from "semantic-release";

// semantic-release publishes Git/GitHub releases; npm publishing is disabled
// by the explicit plugin list. Its structured result drives the Docker job.
export async function runRelease({ release = semanticRelease, env = process.env } = {}) {
  if (!env.GITHUB_OUTPUT) {
    throw new Error("GITHUB_OUTPUT is required; run releases through the main-branch GitHub workflow");
  }

  await appendFile(env.GITHUB_OUTPUT, "new_release_published=false\n");
  const result = await release({}, { env });
  if (!result) {
    return false;
  }

  const { version, gitTag } = result.nextRelease ?? {};
  if (typeof version !== "string" || !/^\d+\.\d+\.\d+$/.test(version) || gitTag !== `v${version}`) {
    throw new Error("semantic-release returned an invalid stable version or Git tag");
  }

  await appendFile(env.GITHUB_OUTPUT, [
    "new_release_published=true",
    `new_release_version=${version}`,
    `new_release_git_tag=${gitTag}`,
    "",
  ].join("\n"));
  return result;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    await runRelease();
  } catch (error) {
    console.error(error);
    process.exitCode = 1;
  }
}
