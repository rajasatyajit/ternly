#!/usr/bin/env bash
# Phase C: measure the routing fixes locally (cloud quota exhausted). ~3 h.
set -u
S=$JOB_TMP/phase-c/fix/suite
cd $REPO
L="-models auto -args --local-only -parallel 1 -timeout 15m -runs 1"
B='^(lru-evict-order|lru-resize|mi-split-maxsplit|mi-rstrip|mitt-off|semver-caret|semver-tilde|ternly-wilson)$'
$S run $L -tasks "$B" -arm fix-classifier -env TERNLY_ROUTING_FIX=classifier -out $JOB_TMP/phase-c/fix >> $JOB_TMP/phase-c/fix-plan.log 2>&1
$S run $L -tasks "$B" -arm fix-textcall -env TERNLY_ROUTING_FIX=textcall -out $JOB_TMP/phase-c/fix >> $JOB_TMP/phase-c/fix-plan.log 2>&1
$S run $L -arm fix-both -out $JOB_TMP/phase-c/fix >> $JOB_TMP/phase-c/fix-plan.log 2>&1
echo plan-done >> $JOB_TMP/phase-c/fix-plan.log
