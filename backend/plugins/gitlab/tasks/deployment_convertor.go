/*
Licensed to the Apache Software Foundation (ASF) under one or more
contributor license agreements.  See the NOTICE file distributed with
this work for additional information regarding copyright ownership.
The ASF licenses this file to You under the Apache License, Version 2.0
(the "License"); you may not use this file except in compliance with
the License.  You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tasks

import (
	"fmt"
	"github.com/apache/incubator-devlake/core/dal"
	"github.com/apache/incubator-devlake/core/errors"
	"github.com/apache/incubator-devlake/core/models/domainlayer"
	"github.com/apache/incubator-devlake/core/models/domainlayer/devops"
	"github.com/apache/incubator-devlake/core/models/domainlayer/didgen"
	"github.com/apache/incubator-devlake/core/plugin"
	"github.com/apache/incubator-devlake/helpers/pluginhelper/api"
	"github.com/apache/incubator-devlake/plugins/gitlab/models"
	"github.com/spf13/cast"
	"reflect"
)

var _ plugin.SubTaskEntryPoint = ConvertDeployment

func init() {
	RegisterSubtaskMeta(ConvertDeploymentMeta)
}

var ConvertDeploymentMeta = &plugin.SubTaskMeta{
	Name:             "ConvertDeployment",
	EntryPoint:       ConvertDeployment,
	EnabledByDefault: true,
	Description:      "Convert gitlab deployment from tool layer to domain layer",
	DomainTypes:      []string{plugin.DOMAIN_TYPE_CICD},
	Dependencies:     []*plugin.SubTaskMeta{ExtractDeploymentMeta},
}

func ConvertDeployment(taskCtx plugin.SubTaskContext) errors.Error {
	rawDataSubTaskArgs, data := CreateRawDataSubTaskArgs(taskCtx, RAW_DEPLOYMENT)
	db := taskCtx.GetDal()

	repo := &models.GitlabProject{}
	err := db.First(repo, dal.Where("gitlab_id = ? and connection_id = ?", data.Options.ProjectId, data.Options.ConnectionId))
	if err != nil {
		return err
	}

	projectIdGen := didgen.NewDomainIdGenerator(&models.GitlabProject{})

	cursor, err := db.Cursor(
		dal.From(&models.GitlabDeployment{}),
		dal.Where("connection_id = ? AND gitlab_id = ?", data.Options.ConnectionId, data.Options.ProjectId),
	)
	if err != nil {
		return err
	}
	defer cursor.Close()

	idGen := didgen.NewDomainIdGenerator(&models.GitlabDeployment{})
	//pipelineIdGen := didgen.NewDomainIdGenerator(&models.BitbucketPipeline{})

	converter, err := api.NewDataConverter(api.DataConverterArgs{
		InputRowType:       reflect.TypeOf(models.GitlabDeployment{}),
		Input:              cursor,
		RawDataSubTaskArgs: *rawDataSubTaskArgs,
		Convert: func(inputRow interface{}) ([]interface{}, errors.Error) {
			gitlabDeployment := inputRow.(*models.GitlabDeployment)

			var duration *uint64
			if gitlabDeployment.DeployableDuration != nil {
				deployableDuration := cast.ToUint64(*gitlabDeployment.DeployableDuration)
				duration = &deployableDuration
			}
			if duration == nil || *duration == 0 {
				if gitlabDeployment.DeployableFinishedAt != nil && gitlabDeployment.DeployableCreatedAt != nil {
					deployableDuration := uint64(gitlabDeployment.DeployableFinishedAt.Sub(*gitlabDeployment.DeployableCreatedAt).Seconds())
					duration = &deployableDuration
				}
			}
			domainDeployCommit := &devops.CicdDeploymentCommit{
				DomainEntity:     domainlayer.NewDomainEntity(idGen.Generate(data.Options.ConnectionId, gitlabDeployment.DeploymentId)),
				CicdScopeId:      projectIdGen.Generate(data.Options.ConnectionId, data.Options.ProjectId),
				CicdDeploymentId: idGen.Generate(data.Options.ConnectionId, gitlabDeployment.DeploymentId),
				Name:             fmt.Sprintf("%s:%d", gitlabDeployment.Name, gitlabDeployment.DeploymentId),
				Result: devops.GetResult(&devops.ResultRule{
					Failed:  []string{"UNDEPLOYED", "failed"},
					Success: []string{"COMPLETED", "success"},
					Abort:   []string{"created", "canceled"},
					Manual:  []string{"running", "blocked"},
					Default: gitlabDeployment.Status,
				}, gitlabDeployment.Status),
				Status: devops.GetStatus(&devops.StatusRule[string]{
					Done:       []string{"COMPLETED", "UNDEPLOYED", "failed", "success", "canceled"},
					InProgress: []string{"running"},
					NotStarted: []string{"created"},
					Manual:     []string{"blocked"},
					Default:    gitlabDeployment.Status,
				}, gitlabDeployment.Status),
				Environment:  gitlabDeployment.Environment,
				CreatedDate:  gitlabDeployment.CreatedDate,
				StartedDate:  gitlabDeployment.DeployableStartedAt,
				FinishedDate: gitlabDeployment.DeployableFinishedAt,
				CommitSha:    gitlabDeployment.Sha,
				RefName:      gitlabDeployment.Ref,
				RepoId:       didgen.NewDomainIdGenerator(&models.GitlabProject{}).Generate(data.Options.ConnectionId, data.Options.ProjectId),
				RepoUrl:      repo.WebUrl,
			}
			if duration != nil {
				domainDeployCommit.DurationSec = duration
			}
			if data.RegexEnricher != nil {
				domainDeployCommit.Environment = data.RegexEnricher.ReturnNameIfOmittedOrMatched(devops.PRODUCTION, gitlabDeployment.Environment)
			}

			return []interface{}{
				domainDeployCommit,
				domainDeployCommit.ToDeployment(),
			}, nil
		},
	})

	if err != nil {
		return err
	}

	return converter.Execute()
}
