/**
 * Vela Pipeline State Helpers
 * Shared utilities for reading and managing pipeline state across hooks.
 */

const fs = require('fs');
const path = require('path');

/**
 * Find the most recent active (non-completed) pipeline state.
 * Searches .vela/artifacts/{date}/{slug}/pipeline-state.json
 */
function findActivePipeline(velaDir) {
  const artifactsDir = path.join(velaDir, 'artifacts');
  if (!fs.existsSync(artifactsDir)) return null;

  try {
    const dateDirs = fs.readdirSync(artifactsDir)
      .filter(d => /^\d{4}-\d{2}-\d{2}$/.test(d))
      .sort()
      .reverse();

    for (const dateDir of dateDirs) {
      const datePath = path.join(artifactsDir, dateDir);
      const slugDirs = fs.readdirSync(datePath)
        .filter(d => fs.statSync(path.join(datePath, d)).isDirectory())
        .sort()
        .reverse();

      for (const slugDir of slugDirs) {
        const statePath = path.join(datePath, slugDir, 'pipeline-state.json');
        if (!fs.existsSync(statePath)) continue;

        try {
          const state = JSON.parse(fs.readFileSync(statePath, 'utf-8'));
          if (state.status === 'completed' || state.status === 'cancelled') continue;

          // Mark stale if untouched for 24 hours
          const mtime = fs.statSync(statePath).mtimeMs;
          if (Date.now() - mtime > 24 * 60 * 60 * 1000) {
            state._stale = true;
          }

          state._path = statePath;
          state._artifactDir = path.join(datePath, slugDir);
          return state;
        } catch (e) {
          continue;
        }
      }
    }
  } catch (e) {
    return null;
  }

  return null;
}

/**
 * Check if an active pipeline exists in the project.
 */
function hasActivePipeline(cwd) {
  const velaDir = path.join(cwd, '.vela');
  if (!fs.existsSync(velaDir)) return false;
  return findActivePipeline(velaDir) !== null;
}

/**
 * Read the Vela config from the project.
 */
function readConfig(cwd) {
  const configPath = path.join(cwd, '.vela', 'config.json');
  if (!fs.existsSync(configPath)) return null;
  try {
    return JSON.parse(fs.readFileSync(configPath, 'utf-8'));
  } catch (e) {
    return null;
  }
}

/**
 * Read pipeline definition.
 */
function readPipelineDefinition(cwd) {
  const pipelinePath = path.join(cwd, '.vela', 'templates', 'pipeline.json');
  if (!fs.existsSync(pipelinePath)) return null;
  try {
    return JSON.parse(fs.readFileSync(pipelinePath, 'utf-8'));
  } catch (e) {
    return null;
  }
}

/**
 * Get the current step definition from the pipeline.
 */
function getCurrentStepDef(pipelineDef, state) {
  if (!pipelineDef || !state) return null;
  const pipelineType = state.pipeline_type || 'standard';
  const pipeline = pipelineDef.pipelines[pipelineType];
  if (!pipeline) return null;

  let steps = pipeline.steps;
  if (pipeline.inherits && pipeline.steps_only) {
    const parent = pipelineDef.pipelines[pipeline.inherits];
    if (parent) {
      steps = parent.steps.filter(s => pipeline.steps_only.includes(s.id));
      // Apply overrides
      if (pipeline.overrides) {
        steps = steps.map(s => {
          if (pipeline.overrides[s.id]) {
            return { ...s, ...pipeline.overrides[s.id] };
          }
          return s;
        });
      }
    }
  }

  return steps.find(s => s.id === state.current_step) || null;
}

/**
 * Get the mode for the current pipeline step.
 */
function getCurrentMode(cwd) {
  const velaDir = path.join(cwd, '.vela');
  const state = findActivePipeline(velaDir);
  if (!state) return 'read'; // Default to read-only when no pipeline

  const pipelineDef = readPipelineDefinition(cwd);
  const stepDef = getCurrentStepDef(pipelineDef, state);
  if (!stepDef) return 'read';

  return stepDef.mode || 'read';
}

/**
 * Get session state file path (project-local).
 */
function getSessionStatePath(sessionId, cwd) {
  const velaStateDir = path.join(cwd || process.cwd(), '.vela', 'state');
  return path.join(velaStateDir, `vela-${sessionId}.json`);
}

module.exports = {
  findActivePipeline,
  hasActivePipeline,
  readConfig,
  readPipelineDefinition,
  getCurrentStepDef,
  getCurrentMode,
  getSessionStatePath
};
