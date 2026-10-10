#!/usr/bin/env bash
# Phase C local measurements (cloud quota exhausted): run after the first local pass.
set -u
S=/home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/local/suite
cd /home/satyajit/workspace/ternly/.claude/worktrees/phase-c
L="-models auto -args --local-only -parallel 1 -timeout 15m"
$S run $L -runs 1 -arm local-only -out /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/local2 >> /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/local-plan.log 2>&1
for A in plan_first best_of=2 no_verify_escalation outline_reads no_schema_repair; do
  $S run $L -runs 1 -arm "$A" -env "TERNLY_LEVERS=$A" -out /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/levers >> /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/local-plan.log 2>&1
done
$S run $L -runs 1 -arm no_fact_checks -env TERNLY_NO_FACT_CHECKS=1 -out /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/levers >> /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/local-plan.log 2>&1
T='^(lru-peek-expiry|mi-chunk-weight|semver-caret|ternly-wilson)$'
$S run $L -runs 5 -tasks "$T" -arm sampled -out /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/repro >> /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/local-plan.log 2>&1
$S run $L -runs 5 -tasks "$T" -arm det -env TERNLY_DETERMINISTIC=1,TERNLY_RESPONSE_CACHE=0 -out /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/repro >> /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/local-plan.log 2>&1
$S run $L -runs 5 -tasks "$T" -stable-ws -arm det-cache -env TERNLY_DETERMINISTIC=1,TERNLY_RESPONSE_CACHE_DIR=/home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/repro-cache -out /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/repro >> /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/local-plan.log 2>&1
for A in plan_first best_of=2 no_verify_escalation outline_reads no_schema_repair; do
  $S run $L -runs 1 -arm "$A" -env "TERNLY_LEVERS=$A" -out /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/levers >> /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/local-plan.log 2>&1
done
$S run $L -runs 1 -arm no_fact_checks -env TERNLY_NO_FACT_CHECKS=1 -out /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/levers >> /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/local-plan.log 2>&1
echo plan-done >> /home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/local-plan.log
